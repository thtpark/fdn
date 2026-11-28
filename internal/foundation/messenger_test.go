package foundation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"

	"github.com/thtpark/fdn/internal/domain"
)

func TestMessengerRoomsDirectGroupProjectMediaRepliesAndReadReceipts(t *testing.T) {
	ctx := context.Background()
	f := newSecurityFixture(t)
	store := f.s
	founderID := f.founder.AgentID
	a1Ident, _, _ := f.register(t, "Agent1", 4101)
	a2Ident, _, _ := f.register(t, "Agent2", 4102)
	a3Ident, _, _ := f.register(t, "Agent3", 4103)
	agent1, err := store.Agent(ctx, founderID, a1Ident.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	agent2, err := store.Agent(ctx, founderID, a2Ident.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	agent3, err := store.Agent(ctx, founderID, a3Ident.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	founder, err := store.Agent(ctx, founderID, founderID)
	if err != nil {
		t.Fatal(err)
	}

	// 1. 1:1 Direct room is canonical & idempotent regardless of caller order.
	direct1, err := store.CreateRoom(ctx, founder.ID, domain.RoomCreateRequest{
		Command:   domain.Command{OperationID: domain.NewID("op")},
		Kind:      domain.RoomKindDirect,
		MemberIDs: []string{agent1.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	direct2, err := store.CreateRoom(ctx, agent1.ID, domain.RoomCreateRequest{
		Command:   domain.Command{OperationID: domain.NewID("op")},
		Kind:      domain.RoomKindDirect,
		MemberIDs: []string{founder.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if direct1.ID != direct2.ID || direct1.Kind != domain.RoomKindDirect || len(direct1.MemberIDs) != 2 {
		t.Fatalf("unexpected direct room mismatch: %+v vs %+v", direct1, direct2)
	}
	if _, err := store.Room(ctx, agent2.ID, direct1.ID); domain.AsFault(err).Code != domain.Denied {
		t.Fatalf("outsider accessed private 1:1 room: %v", err)
	}

	// Send 1:1 message and verify unread count, delivery queue, reply-to, and read acknowledgement.
	msg1, err := store.SendRoomMessage(ctx, founder.ID, direct1.ID, domain.RoomMessageRequest{
		Command: domain.Command{OperationID: domain.NewID("op")},
		Body:    "Hello Agent1, ready for check-in?",
		Origin:  "human",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(msg1.TargetAgentIDs, []string{agent1.ID}) {
		t.Fatalf("expected Agent1 as 1:1 target, got %+v", msg1.TargetAgentIDs)
	}
	a1Rooms, err := store.ListRooms(ctx, agent1.ID, domain.RoomKindDirect, "")
	if err != nil || len(a1Rooms) != 1 || a1Rooms[0].UnreadCount != 1 {
		t.Fatalf("expected 1 unread in Agent1 direct room, got %+v (err=%v)", a1Rooms, err)
	}
	deliveries, err := store.RoomDeliveries(ctx, agent1.ID, "", 10, false)
	if err != nil || len(deliveries.Items) != 1 || deliveries.Items[0].Message.ID != msg1.ID {
		t.Fatalf("expected 1 pending delivery for Agent1, got %+v (err=%v)", deliveries, err)
	}
	readMsg, err := store.ReadRoomMessage(ctx, agent1.ID, msg1.ID, domain.MessageReadRequest{
		Command: domain.Command{OperationID: domain.NewID("op")},
	})
	if err != nil || len(readMsg.ReadBy) != 1 || readMsg.ReadBy[0].AgentID != agent1.ID {
		t.Fatalf("expected read receipt from Agent1, got %+v (err=%v)", readMsg, err)
	}
	reply1, err := store.SendRoomMessage(ctx, agent1.ID, direct1.ID, domain.RoomMessageRequest{
		Command:   domain.Command{OperationID: domain.NewID("op")},
		Body:      "Ready and online!",
		Origin:    "ai",
		ReplyToID: msg1.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply1.ReplyToID != msg1.ID || reply1.ReplyPreview == nil || reply1.ReplyPreview.SenderID != founder.ID {
		t.Fatalf("missing reply preview on reply message: %+v", reply1)
	}

	// 2. Group room with combination of agents + embedded PNG media + member addition.
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d,
		0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
	sum := sha256.Sum256(pngBytes)
	digest := hex.EncodeToString(sum[:])
	staged, err := store.BeginFile(ctx, founder.ID, domain.FileUploadRequest{
		Command:   domain.Command{OperationID: domain.NewID("op")},
		Name:      "architecture.png",
		MediaType: "image/png",
		Size:      int64(len(pngBytes)),
		SHA256:    digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutFileChunk(ctx, founder.ID, staged.ID, domain.FileChunkRequest{
		Command: domain.Command{OperationID: domain.NewID("op")},
		Offset:  0,
		Data:    pngBytes,
		SHA256:  digest,
	}); err != nil {
		t.Fatal(err)
	}
	readyFile, err := store.FinalizeFile(ctx, founder.ID, staged.ID, domain.FileChangeRequest{
		Command: domain.Command{OperationID: domain.NewID("op")},
		Reason:  "Share screenshot in group room",
	})
	if err != nil {
		t.Fatal(err)
	}

	group, err := store.CreateRoom(ctx, founder.ID, domain.RoomCreateRequest{
		Command:   domain.Command{OperationID: domain.NewID("op")},
		Kind:      domain.RoomKindGroup,
		Name:      "Backend Squad",
		MemberIDs: []string{agent1.ID, agent2.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Unmentioned human message -> prompts BOTH Agent1 and Agent2.
	broadcastMsg, err := store.SendRoomMessage(ctx, founder.ID, group.ID, domain.RoomMessageRequest{
		Command:       domain.Command{OperationID: domain.NewID("op")},
		Body:          "Review this diagram ![arch](foundation-file://" + readyFile.ID + ")",
		Origin:        "human",
		AttachmentIDs: []string{readyFile.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(broadcastMsg.TargetAgentIDs) != 2 || !slices.Contains(broadcastMsg.TargetAgentIDs, agent1.ID) || !slices.Contains(broadcastMsg.TargetAgentIDs, agent2.ID) {
		t.Fatalf("expected unmentioned group message to target both Agent1 and Agent2, got %+v", broadcastMsg.TargetAgentIDs)
	}
	// Both Agent1 and Agent2 received automatic file read grants.
	for _, reader := range []string{agent1.ID, agent2.ID} {
		chunk, err := store.FileChunk(ctx, reader, readyFile.ID, 0)
		if err != nil || !bytes.Equal(chunk.Data, pngBytes) {
			t.Fatalf("group member %s could not read attached inline image: %v", reader, err)
		}
	}

	// Mention-targeted message in group room -> ONLY mentioned agent is targeted!
	mentionMsg, err := store.SendRoomMessage(ctx, founder.ID, group.ID, domain.RoomMessageRequest{
		Command:   domain.Command{OperationID: domain.NewID("op")},
		Body:      "@Agent2 please take point on the parser.",
		Origin:    "human",
		ReplyToID: broadcastMsg.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mentionMsg.Mentions, []string{agent2.ID}) || !slices.Equal(mentionMsg.TargetAgentIDs, []string{agent2.ID}) {
		t.Fatalf("expected only Agent2 to be targeted on @Agent2 mention, got mentions=%v targets=%v", mentionMsg.Mentions, mentionMsg.TargetAgentIDs)
	}
	// Adding Agent3 to the group room grants Agent3 access to the room and its existing image attachment.
	updatedGroup, err := store.ChangeRoom(ctx, founder.ID, group.ID, domain.RoomChangeRequest{
		Command:      domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: group.Revision + 2},
		AddMemberIDs: []string{agent3.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(updatedGroup.MemberIDs, agent3.ID) {
		t.Fatalf("Agent3 not added to group: %+v", updatedGroup)
	}
	if chunk, err := store.FileChunk(ctx, agent3.ID, readyFile.ID, 0); err != nil || !bytes.Equal(chunk.Data, pngBytes) {
		t.Fatalf("newly added group member Agent3 could not read room attachment: %v", err)
	}

	// 3. Dedicated Project room automatically appears for active projects and syncs project team.
	project, err := store.CreateProject(ctx, founder.ID, domain.ProjectRequest{
		Command:     domain.Command{OperationID: domain.NewID("op")},
		Name:        "Messenger Launch",
		Description: "Realtime messenger rollout",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delegate(ctx, founder.ID, project.ID, domain.GrantRequest{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: project.Revision},
		AgentID: agent1.ID,
	}); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(ctx, founder.ID, domain.TaskRequest{
		Command:   domain.Command{OperationID: domain.NewID("op")},
		ProjectID: project.ID,
		Title:     "Build UI",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeTask(ctx, founder.ID, task.ID, domain.TaskChange{
		Command: domain.Command{OperationID: domain.NewID("op"), ExpectedRevision: task.Revision},
		Kind:    "assign",
		AgentID: agent2.ID,
		Reason:  "Assign UI work",
	}); err != nil {
		t.Fatal(err)
	}

	projRooms, err := store.ListRooms(ctx, founder.ID, domain.RoomKindProject, project.ID)
	if err != nil || len(projRooms) != 1 {
		t.Fatalf("expected dedicated project room for %s, got %+v (err=%v)", project.ID, projRooms, err)
	}
	pRoom := projRooms[0]
	for _, expected := range []string{founder.ID, agent1.ID, agent2.ID} {
		if !slices.Contains(pRoom.MemberIDs, expected) {
			t.Fatalf("project team member %s missing from project room %+v", expected, pRoom)
		}
	}
	if slices.Contains(pRoom.MemberIDs, agent3.ID) {
		t.Fatalf("non-project agent3 should not automatically be in project room: %+v", pRoom)
	}

	// Unmentioned human message in project room targets all project-team AI members (Agent1 & Agent2).
	projMsg, err := store.SendRoomMessage(ctx, founder.ID, pRoom.ID, domain.RoomMessageRequest{
		Command: domain.Command{OperationID: domain.NewID("op")},
		Body:    "Kickoff check for everyone in Messenger Launch!",
		Origin:  "human",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(projMsg.TargetAgentIDs) != 2 || !slices.Contains(projMsg.TargetAgentIDs, agent1.ID) || !slices.Contains(projMsg.TargetAgentIDs, agent2.ID) {
		t.Fatalf("expected project broadcast to target Agent1 and Agent2, got %+v", projMsg.TargetAgentIDs)
	}
	readRes, err := store.ReadRoom(ctx, agent2.ID, pRoom.ID, domain.RoomReadRequest{
		Command: domain.Command{OperationID: domain.NewID("op")},
	})
	if err != nil || readRes.ReadCount != 1 {
		t.Fatalf("expected ReadRoom to mark 1 message read, got %+v (err=%v)", readRes, err)
	}
}
