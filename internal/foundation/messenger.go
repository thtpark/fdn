package foundation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/thtpark/fdn/internal/domain"
)

var bareMentionPattern = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_./@:-])@([a-zA-Z0-9_-]{1,128})\b`)

func saveRoom(ctx context.Context, tx *sql.Tx, room domain.MessageRoom) error {
	stored := room
	stored.UnreadCount, stored.MentionCount, stored.LastReadAt = 0, 0, 0
	data, err := encode(stored)
	if err != nil {
		return err
	}
	var directKey any
	if stored.Kind == domain.RoomKindDirect && len(stored.MemberIDs) == 2 {
		directKey = domain.DirectPairKey(stored.MemberIDs[0], stored.MemberIDs[1])
	}
	var projectID any
	if stored.Kind == domain.RoomKindProject && stored.ProjectID != "" {
		projectID = stored.ProjectID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO message_rooms(id,data,kind,direct_key,project_id,updated_at)
VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,updated_at=excluded.updated_at`,
		stored.ID, data, stored.Kind, directKey, projectID, stored.UpdatedAt)
	return err
}

func projectTeamMembers(ctx context.Context, q queryer, project domain.Project, added []string) ([]string, error) {
	candidates := map[string]bool{project.OwnerID: true}
	for _, id := range project.Delegates {
		candidates[id] = true
	}
	for _, id := range added {
		candidates[id] = true
	}
	taskRows, err := q.QueryContext(ctx, `SELECT data FROM tasks WHERE project_id=? AND archived=0 AND status NOT IN (?,?)`,
		project.ID, domain.WontDo, domain.CancelledStatus)
	if err != nil {
		return nil, err
	}
	tasks, err := scanJSON[domain.Task](taskRows)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.AssigneeID != "" {
			candidates[t.AssigneeID] = true
		}
		if t.ActiveRun != nil && t.ActiveRun.AgentID != "" {
			candidates[t.ActiveRun.AgentID] = true
		}
	}
	agentRows, err := q.QueryContext(ctx, "SELECT data FROM agents WHERE active=1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	agents, err := scanJSON[domain.Agent](agentRows)
	if err != nil {
		return nil, err
	}
	members := make([]string, 0, len(candidates)+1)
	for _, a := range agents {
		if a.Banned {
			continue
		}
		if a.Founder || candidates[a.ID] {
			members = append(members, a.ID)
		}
	}
	slices.Sort(members)
	return slices.Compact(members), nil
}

func virtualProjectRoom(project domain.Project, members []string) domain.MessageRoom {
	return domain.MessageRoom{
		ID:        domain.ProjectRoomID(project.ID),
		Kind:      domain.RoomKindProject,
		Name:      project.Name,
		ProjectID: project.ID,
		MemberIDs: members,
		CreatedBy: project.OwnerID,
		CreatedAt: project.CreatedAt,
		UpdatedAt: project.CreatedAt,
		Revision:  1,
	}
}

func loadOrSynthesizeRoom(ctx context.Context, q queryer, id string) (domain.MessageRoom, bool, error) {
	room, err := get[domain.MessageRoom](ctx, q, "message_rooms", id)
	if err == nil {
		return room, true, nil
	}
	if domain.AsFault(err).Code != domain.NotFound {
		return domain.MessageRoom{}, false, err
	}
	rows, queryErr := q.QueryContext(ctx, "SELECT data FROM projects")
	if queryErr != nil {
		return domain.MessageRoom{}, false, queryErr
	}
	projects, scanErr := scanJSON[domain.Project](rows)
	if scanErr != nil {
		return domain.MessageRoom{}, false, scanErr
	}
	for _, p := range projects {
		if p.ID == domain.AdhocProjectID || p.Archived {
			continue
		}
		if domain.ProjectRoomID(p.ID) == id {
			members, memberErr := projectTeamMembers(ctx, q, p, nil)
			if memberErr != nil {
				return domain.MessageRoom{}, false, memberErr
			}
			return virtualProjectRoom(p, members), false, nil
		}
	}
	return domain.MessageRoom{}, false, err
}

func hydrateRoom(ctx context.Context, q queryer, actor string, room *domain.MessageRoom) (bool, error) {
	if room.Kind == domain.RoomKindProject {
		project, err := get[domain.Project](ctx, q, "projects", room.ProjectID)
		if err != nil {
			return false, err
		}
		if project.Archived || project.ID == domain.AdhocProjectID {
			return false, nil
		}
		members, err := projectTeamMembers(ctx, q, project, room.AddedMemberIDs)
		if err != nil {
			return false, err
		}
		room.Name = project.Name
		room.MemberIDs = members
	} else {
		agentRows, err := q.QueryContext(ctx, "SELECT data FROM agents WHERE active=1")
		if err != nil {
			return false, err
		}
		agents, err := scanJSON[domain.Agent](agentRows)
		if err != nil {
			return false, err
		}
		active := make(map[string]bool, len(agents))
		for _, a := range agents {
			if !a.Banned {
				active[a.ID] = true
			}
		}
		filtered := make([]string, 0, len(room.MemberIDs))
		for _, id := range room.MemberIDs {
			if active[id] || room.Kind == domain.RoomKindDirect {
				filtered = append(filtered, id)
			}
		}
		slices.Sort(filtered)
		room.MemberIDs = slices.Compact(filtered)
	}
	if actor != "" {
		var unread int
		var lastRead sql.NullInt64
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN read_at=0 THEN 1 ELSE 0 END),0), MAX(read_at)
FROM room_deliveries WHERE room_id=? AND agent_id=?`, room.ID, actor).Scan(&unread, &lastRead); err != nil {
			return false, err
		}
		var mentions int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM room_deliveries d
JOIN room_messages m ON m.id=d.message_id
WHERE d.room_id=? AND d.agent_id=? AND d.read_at=0
AND EXISTS(SELECT 1 FROM json_each(m.data,'$.mentions') WHERE value=?)`, room.ID, actor, actor).Scan(&mentions); err != nil {
			return false, err
		}
		room.UnreadCount = unread
		room.MentionCount = mentions
		room.LastReadAt = lastRead.Int64
	}
	return true, nil
}

func roomMessagePreview(body string, attachments []domain.FileReference) string {
	cleaned := strings.TrimSpace(body)
	if cleaned != "" {
		// Replace inline foundation-file markdown tags with a concise label.
		cleaned = inlineAttachmentPattern.ReplaceAllString(cleaned, "[$1]")
		cleaned = strings.Join(strings.Fields(cleaned), " ")
		if len(cleaned) > 160 {
			cleaned = cleaned[:157] + "..."
		}
		return cleaned
	}
	if len(attachments) == 1 {
		return "Shared " + attachments[0].Name
	}
	if len(attachments) > 1 {
		return fmt.Sprintf("Shared %d attachments", len(attachments))
	}
	return ""
}

var inlineAttachmentPattern = regexp.MustCompile(`!\[([^\]\r\n]*)\]\(foundation-file://file_[0-9a-f]{32}\)`)

func resolveRoomMentions(ctx context.Context, tx *sql.Tx, body, author string) ([]string, error) {
	mentions, err := resolveMentions(ctx, tx, body, author)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT data FROM agents WHERE active=1")
	if err != nil {
		return nil, err
	}
	agents, err := scanJSON[domain.Agent](rows)
	if err != nil {
		return nil, err
	}
	byName := make(map[string][]string, len(agents))
	byID := make(map[string]bool, len(agents))
	for _, a := range agents {
		if a.Banned {
			continue
		}
		byID[a.ID] = true
		key := strings.ToLower(a.Name)
		byName[key] = append(byName[key], a.ID)
	}
	for _, match := range bareMentionPattern.FindAllStringSubmatch(body, -1) {
		token := match[1]
		resolved := ""
		if mentionIDPattern.MatchString(token) && byID[token] {
			resolved = token
		} else if matches := byName[strings.ToLower(token)]; len(matches) == 1 {
			resolved = matches[0]
		}
		if resolved != "" && resolved != author && !slices.Contains(mentions, resolved) {
			mentions = append(mentions, resolved)
		}
	}
	slices.Sort(mentions)
	return mentions, nil
}

func (s *Store) resolveRoomAttachments(ctx context.Context, tx *sql.Tx, actor string, members, ids []string) ([]domain.FileReference, error) {
	if len(ids) > domain.MaxAttachments {
		return nil, domain.Fail(domain.Invalid, "at most 32 files may be attached")
	}
	references := make([]domain.FileReference, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	now := s.timestamp()
	for _, id := range ids {
		if seen[id] {
			return nil, domain.Fail(domain.Invalid, "duplicate attachment")
		}
		seen[id] = true
		file, err := readFile(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if err := requireFileReader(ctx, tx, actor, file); err != nil {
			return nil, err
		}
		if file.State != "ready" {
			return nil, domain.Fail(domain.Prerequisite, "only finalized available content may be attached")
		}
		if !file.TeamReadable {
			if file.TransferTo != "" {
				if len(members) != 2 || !slices.Contains(members, file.TransferTo) || !slices.Contains(members, file.OwnerID) {
					return nil, domain.Fail(domain.Denied, "private transfer attachment must stay with its designated recipient")
				}
			}
			grantedAny := false
			for _, member := range members {
				if member == actor || member == file.OwnerID {
					continue
				}
				if actor == file.OwnerID {
					grant := domain.FileGrant{FileID: id, AgentID: member, GrantedBy: actor, GrantedAt: now}
					changed, err := saveFileGrant(ctx, tx, grant)
					if err != nil {
						return nil, err
					}
					if changed {
						grantedAny = true
					}
				} else if err := requireFileReader(ctx, tx, member, file); err != nil {
					if domain.AsFault(err).Code != domain.Denied {
						return nil, err
					}
					return nil, domain.Fail(domain.Denied, "a reader cannot re-share private content to room members without owner grants")
				}
			}
			if grantedAny {
				file.Revision++
				if err := saveFile(ctx, tx, file); err != nil {
					return nil, err
				}
				if err := fileUpdate(ctx, tx, file); err != nil {
					return nil, err
				}
			}
		}
		references = append(references, file.FileReference)
	}
	return references, nil
}

func (s *Store) grantRoomFilesToMembers(ctx context.Context, tx *sql.Tx, roomID string, members []string) error {
	if len(members) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT data FROM room_messages WHERE room_id=?", roomID)
	if err != nil {
		return err
	}
	messages, err := scanJSON[domain.RoomMessage](rows)
	if err != nil {
		return err
	}
	fileIDs := make(map[string]bool)
	for _, m := range messages {
		for _, att := range m.Attachments {
			fileIDs[att.ID] = true
		}
	}
	now := s.timestamp()
	for id := range fileIDs {
		file, err := readFile(ctx, tx, id)
		if err != nil {
			if domain.AsFault(err).Code == domain.NotFound {
				continue
			}
			return err
		}
		if file.State != "ready" || file.TeamReadable || file.TransferTo != "" || file.RegistryID != "" {
			continue
		}
		changedAny := false
		for _, member := range members {
			if member == file.OwnerID {
				continue
			}
			if _, subErr := activeIdentitySubject(ctx, tx, member); subErr != nil {
				continue
			}
			changed, err := saveFileGrant(ctx, tx, domain.FileGrant{
				FileID: id, AgentID: member, GrantedBy: file.OwnerID, GrantedAt: now,
			})
			if err != nil {
				return err
			}
			if changed {
				changedAny = true
			}
		}
		if changedAny {
			file.Revision++
			if err := saveFile(ctx, tx, file); err != nil {
				return err
			}
			if err := fileUpdate(ctx, tx, file); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) syncProjectRoomFileGrants(ctx context.Context, tx *sql.Tx, projectID string) error {
	if projectID == "" || projectID == domain.AdhocProjectID {
		return nil
	}
	roomID := domain.ProjectRoomID(projectID)
	room, err := get[domain.MessageRoom](ctx, tx, "message_rooms", roomID)
	if err != nil {
		if domain.AsFault(err).Code == domain.NotFound {
			return nil
		}
		return err
	}
	project, err := get[domain.Project](ctx, tx, "projects", projectID)
	if err != nil || project.Archived {
		return nil
	}
	members, err := projectTeamMembers(ctx, tx, project, room.AddedMemberIDs)
	if err != nil {
		return err
	}
	return s.grantRoomFilesToMembers(ctx, tx, roomID, members)
}

func (s *Store) CreateRoom(ctx context.Context, actor string, req domain.RoomCreateRequest) (domain.MessageRoom, error) {
	return mutate(ctx, s, actor, "room.create", req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.MessageRoom, error) {
		var room domain.MessageRoom
		if err := req.Validate(); err != nil {
			return room, err
		}
		now := s.timestamp()
		switch req.Kind {
		case domain.RoomKindDirect:
			peers := make([]string, 0, 2)
			for _, id := range req.MemberIDs {
				if id != a.ID && !slices.Contains(peers, id) {
					peers = append(peers, id)
				}
			}
			if len(peers) != 1 {
				return room, domain.Fail(domain.Invalid, "direct rooms require exactly one other active agent")
			}
			peer := peers[0]
			if _, err := activeIdentitySubject(ctx, tx, peer); err != nil {
				return room, err
			}
			id := domain.DirectRoomID(a.ID, peer)
			existing, err := get[domain.MessageRoom](ctx, tx, "message_rooms", id)
			if err == nil {
				if _, hErr := hydrateRoom(ctx, tx, a.ID, &existing); hErr != nil {
					return room, hErr
				}
				return existing, nil
			}
			if domain.AsFault(err).Code != domain.NotFound {
				return room, err
			}
			members := []string{a.ID, peer}
			slices.Sort(members)
			room = domain.MessageRoom{
				ID: id, Kind: domain.RoomKindDirect, MemberIDs: members,
				CreatedBy: a.ID, CreatedAt: now, UpdatedAt: now, Revision: 1,
			}
			if err := saveRoom(ctx, tx, room); err != nil {
				return room, err
			}
			_, err = hydrateRoom(ctx, tx, a.ID, &room)
			return room, err

		case domain.RoomKindGroup:
			members := []string{a.ID}
			for _, id := range req.MemberIDs {
				if id == a.ID || slices.Contains(members, id) {
					continue
				}
				if _, err := activeIdentitySubject(ctx, tx, id); err != nil {
					return room, err
				}
				members = append(members, id)
			}
			if len(members) < 2 {
				return room, domain.Fail(domain.Invalid, "group rooms require at least one other active agent")
			}
			if len(members) > domain.MaxRoomMembers {
				return room, domain.Fail(domain.Invalid, "group room exceeds the bounded member limit")
			}
			slices.Sort(members)
			room = domain.MessageRoom{
				ID: domain.NewID("room"), Kind: domain.RoomKindGroup,
				Name: strings.TrimSpace(req.Name), MemberIDs: members,
				CreatedBy: a.ID, CreatedAt: now, UpdatedAt: now, Revision: 1,
			}
			if err := saveRoom(ctx, tx, room); err != nil {
				return room, err
			}
			_, err := hydrateRoom(ctx, tx, a.ID, &room)
			return room, err

		case domain.RoomKindProject:
			project, err := get[domain.Project](ctx, tx, "projects", req.ProjectID)
			if err != nil {
				return room, err
			}
			if project.Archived || project.ID == domain.AdhocProjectID {
				return room, domain.Fail(domain.Prerequisite, "project is archived or does not support a shared project room")
			}
			added := make([]string, 0, len(req.MemberIDs))
			for _, id := range req.MemberIDs {
				if !slices.Contains(added, id) {
					if _, err := activeIdentitySubject(ctx, tx, id); err != nil {
						return room, err
					}
					added = append(added, id)
				}
			}
			slices.Sort(added)
			baseMembers, err := projectTeamMembers(ctx, tx, project, nil)
			if err != nil {
				return room, err
			}
			if !slices.Contains(baseMembers, a.ID) && !authority(a, project) {
				return room, domain.Fail(domain.Denied, "project room access requires membership on the project team")
			}
			id := domain.ProjectRoomID(project.ID)
			existing, err := get[domain.MessageRoom](ctx, tx, "message_rooms", id)
			if err == nil {
				for _, extra := range added {
					if !slices.Contains(existing.AddedMemberIDs, extra) {
						existing.AddedMemberIDs = append(existing.AddedMemberIDs, extra)
					}
				}
				slices.Sort(existing.AddedMemberIDs)
				existing.UpdatedAt = now
				existing.Revision++
				if err := saveRoom(ctx, tx, existing); err != nil {
					return room, err
				}
				if err := s.grantRoomFilesToMembers(ctx, tx, existing.ID, added); err != nil {
					return room, err
				}
				_, err = hydrateRoom(ctx, tx, a.ID, &existing)
				return existing, err
			}
			if domain.AsFault(err).Code != domain.NotFound {
				return room, err
			}
			members, err := projectTeamMembers(ctx, tx, project, added)
			if err != nil {
				return room, err
			}
			room = domain.MessageRoom{
				ID: id, Kind: domain.RoomKindProject, Name: project.Name,
				ProjectID: project.ID, MemberIDs: members, AddedMemberIDs: added,
				CreatedBy: a.ID, CreatedAt: now, UpdatedAt: now, Revision: 1,
			}
			if err := saveRoom(ctx, tx, room); err != nil {
				return room, err
			}
			_, err = hydrateRoom(ctx, tx, a.ID, &room)
			return room, err
		default:
			return room, domain.Fail(domain.Invalid, "unsupported room kind")
		}
	})
}

func (s *Store) ChangeRoom(ctx context.Context, actor, id string, req domain.RoomChangeRequest) (domain.MessageRoom, error) {
	return mutate(ctx, s, actor, "room.change:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.MessageRoom, error) {
		if err := req.Validate(); err != nil {
			return domain.MessageRoom{}, err
		}
		room, stored, err := loadOrSynthesizeRoom(ctx, tx, id)
		if err != nil {
			return room, err
		}
		visible, err := hydrateRoom(ctx, tx, a.ID, &room)
		if err != nil {
			return room, err
		}
		if !visible || !slices.Contains(room.MemberIDs, a.ID) {
			return domain.MessageRoom{}, domain.Fail(domain.Denied, "only current room members may update this room")
		}
		if room.Kind == domain.RoomKindDirect {
			return domain.MessageRoom{}, domain.Fail(domain.Invalid, "1:1 direct rooms cannot rename or add members; create a group room instead")
		}
		if req.Name != nil && room.Kind != domain.RoomKindGroup {
			return domain.MessageRoom{}, domain.Fail(domain.Invalid, "project room names track their project title")
		}
		if stored && req.ExpectedRevision != 0 {
			if err := expect(room.Revision, req.ExpectedRevision); err != nil {
				return room, err
			}
		}
		for _, member := range req.AddMemberIDs {
			if _, err := activeIdentitySubject(ctx, tx, member); err != nil {
				return room, err
			}
		}
		if req.Name != nil {
			room.Name = strings.TrimSpace(*req.Name)
		}
		if room.Kind == domain.RoomKindGroup {
			for _, member := range req.AddMemberIDs {
				if !slices.Contains(room.MemberIDs, member) {
					room.MemberIDs = append(room.MemberIDs, member)
				}
			}
			if len(room.MemberIDs) > domain.MaxRoomMembers {
				return room, domain.Fail(domain.Invalid, "group room exceeds the bounded member limit")
			}
			slices.Sort(room.MemberIDs)
		} else if room.Kind == domain.RoomKindProject {
			for _, member := range req.AddMemberIDs {
				if !slices.Contains(room.AddedMemberIDs, member) {
					room.AddedMemberIDs = append(room.AddedMemberIDs, member)
				}
			}
			if len(room.AddedMemberIDs) > domain.MaxRoomMembers {
				return room, domain.Fail(domain.Invalid, "project room exceeds the bounded extra member limit")
			}
			slices.Sort(room.AddedMemberIDs)
		}
		room.UpdatedAt = s.timestamp()
		if stored {
			room.Revision++
		}
		if err := saveRoom(ctx, tx, room); err != nil {
			return room, err
		}
		if err := s.grantRoomFilesToMembers(ctx, tx, room.ID, req.AddMemberIDs); err != nil {
			return room, err
		}
		_, err = hydrateRoom(ctx, tx, a.ID, &room)
		return room, err
	})
}

func (s *Store) ListRooms(ctx context.Context, actor, kind, projectFilter string) (rooms []domain.MessageRoom, err error) {
	rooms = []domain.MessageRoom{}
	if kind != "" && !domain.ValidRoomKind(kind) {
		return rooms, domain.Fail(domain.Invalid, "invalid room kind filter")
	}
	if projectFilter != "" && !domain.ValidOperation(projectFilter) {
		return rooms, domain.Fail(domain.Invalid, "invalid room project filter")
	}
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return rooms, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	a, err := s.actor(ctx, snapshot, actor)
	if err != nil {
		return rooms, err
	}
	if _, active, err := agentOperationIdentity(ctx, snapshot, a); err != nil {
		return rooms, err
	} else if !active {
		return rooms, domain.Fail(domain.Denied, "listing rooms requires a current active identity")
	}
	rows, err := snapshot.QueryContext(ctx, "SELECT data FROM message_rooms ORDER BY updated_at DESC, id")
	if err != nil {
		return rooms, err
	}
	stored, err := scanJSON[domain.MessageRoom](rows)
	if err != nil {
		return rooms, err
	}
	seenProjects := make(map[string]bool)
	for _, room := range stored {
		if room.Kind == domain.RoomKindProject {
			seenProjects[room.ProjectID] = true
		}
		if (kind != "" && room.Kind != kind) || (projectFilter != "" && room.ProjectID != projectFilter) {
			continue
		}
		visible, err := hydrateRoom(ctx, snapshot, actor, &room)
		if err != nil {
			return rooms, err
		}
		if visible && slices.Contains(room.MemberIDs, actor) {
			rooms = append(rooms, room)
		}
	}
	if kind == "" || kind == domain.RoomKindProject {
		projRows, err := snapshot.QueryContext(ctx, "SELECT data FROM projects ORDER BY id")
		if err != nil {
			return rooms, err
		}
		projects, err := scanJSON[domain.Project](projRows)
		if err != nil {
			return rooms, err
		}
		for _, p := range projects {
			if p.ID == domain.AdhocProjectID || p.Archived || seenProjects[p.ID] || (projectFilter != "" && p.ID != projectFilter) {
				continue
			}
			members, err := projectTeamMembers(ctx, snapshot, p, nil)
			if err != nil {
				return rooms, err
			}
			if slices.Contains(members, actor) {
				rooms = append(rooms, virtualProjectRoom(p, members))
			}
		}
	}
	slices.SortStableFunc(rooms, func(left, right domain.MessageRoom) int {
		lt := max(left.LastMessageAt, left.UpdatedAt)
		rt := max(right.LastMessageAt, right.UpdatedAt)
		if lt != rt {
			if lt > rt {
				return -1
			}
			return 1
		}
		return strings.Compare(left.ID, right.ID)
	})
	return rooms, nil
}

func (s *Store) Room(ctx context.Context, actor, id string) (room domain.MessageRoom, err error) {
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return room, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	a, err := s.actor(ctx, snapshot, actor)
	if err != nil {
		return room, err
	}
	if _, active, err := agentOperationIdentity(ctx, snapshot, a); err != nil {
		return room, err
	} else if !active {
		return room, domain.Fail(domain.Denied, "room inspection requires a current active identity")
	}
	room, _, err = loadOrSynthesizeRoom(ctx, snapshot, id)
	if err != nil {
		return room, err
	}
	visible, err := hydrateRoom(ctx, snapshot, actor, &room)
	if err != nil {
		return room, err
	}
	if !visible || !slices.Contains(room.MemberIDs, actor) {
		return domain.MessageRoom{}, domain.Fail(domain.Denied, "room is private to its current members")
	}
	return room, nil
}

func (s *Store) SendRoomMessage(ctx context.Context, actor, roomID string, req domain.RoomMessageRequest) (domain.RoomMessage, error) {
	return mutate(ctx, s, actor, "room.send:"+roomID, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.RoomMessage, error) {
		var msg domain.RoomMessage
		if err := req.Validate(); err != nil {
			return msg, err
		}
		room, stored, err := loadOrSynthesizeRoom(ctx, tx, roomID)
		if err != nil {
			return msg, err
		}
		visible, err := hydrateRoom(ctx, tx, a.ID, &room)
		if err != nil {
			return msg, err
		}
		if !visible || !slices.Contains(room.MemberIDs, a.ID) {
			return msg, domain.Fail(domain.Denied, "only current room members may post messages")
		}
		origin := req.Origin
		if origin == "" {
			if a.Founder && a.Mode == "manual" {
				origin = "human"
			} else {
				origin = "ai"
			}
		}
		responseRequested := origin == "human"
		if req.ResponseRequest != nil {
			responseRequested = *req.ResponseRequest
		}
		mentions, err := resolveRoomMentions(ctx, tx, req.Body, a.ID)
		if err != nil {
			return msg, err
		}
		var preview *domain.RoomReplyPreview
		if req.ReplyToID != "" {
			parent, err := get[domain.RoomMessage](ctx, tx, "room_messages", req.ReplyToID)
			if err != nil {
				return msg, err
			}
			if parent.RoomID != room.ID {
				return msg, domain.Fail(domain.Denied, "reply_to_id must reference a message in the same room")
			}
			snippet := roomMessagePreview(parent.Body, parent.Attachments)
			preview = &domain.RoomReplyPreview{
				ID:              parent.ID,
				SenderID:        parent.SenderID,
				Body:            snippet,
				AttachmentCount: len(parent.Attachments),
			}
		}
		var attachments []domain.FileReference
		if len(req.AttachmentIDs) != 0 {
			attachments, err = s.resolveRoomAttachments(ctx, tx, a.ID, room.MemberIDs, req.AttachmentIDs)
			if err != nil {
				return msg, err
			}
		}
		if err := checkInlineAttachments(req.Body, attachments); err != nil {
			return msg, err
		}
		otherMembers := make([]string, 0, len(room.MemberIDs))
		mentionedMembers := make([]string, 0, len(mentions))
		for _, member := range room.MemberIDs {
			if member == a.ID {
				continue
			}
			if _, subErr := activeIdentitySubject(ctx, tx, member); subErr != nil {
				if room.Kind == domain.RoomKindDirect {
					return msg, subErr
				}
				continue
			}
			otherMembers = append(otherMembers, member)
			if slices.Contains(mentions, member) {
				mentionedMembers = append(mentionedMembers, member)
			}
		}
		targets := []string{}
		if responseRequested {
			switch room.Kind {
			case domain.RoomKindDirect:
				targets = slices.Clone(otherMembers)
			case domain.RoomKindGroup, domain.RoomKindProject:
				if len(mentionedMembers) > 0 {
					targets = slices.Clone(mentionedMembers)
				} else if origin == "human" {
					// In both group and project rooms, unmentioned human messages prompt all active room members.
					targets = slices.Clone(otherMembers)
				}
			}
		}
		now := s.timestamp()
		if !stored {
			room.CreatedAt = now
		}
		room.UpdatedAt = now
		room.LastMessageAt = now
		msg = domain.RoomMessage{
			ID:              domain.NewID("room_message"),
			RoomID:          room.ID,
			RoomKind:        room.Kind,
			ProjectID:       room.ProjectID,
			SenderID:        a.ID,
			Body:            req.Body,
			Origin:          origin,
			Kind:            "chat",
			Urgent:          req.Urgent,
			ResponseRequest: responseRequested,
			ReplyToID:       req.ReplyToID,
			ReplyPreview:    preview,
			Mentions:        mentions,
			TargetAgentIDs:  targets,
			Attachments:     attachments,
			ReadBy:          []domain.RoomReadReceipt{},
			CreatedAt:       now,
			Revision:        1,
		}
		room.LastMessageID = msg.ID
		room.LastMessagePreview = roomMessagePreview(msg.Body, msg.Attachments)
		room.LastSenderID = a.ID
		if stored {
			room.Revision++
		}
		if err := saveRoom(ctx, tx, room); err != nil {
			return msg, err
		}
		msgData, err := encode(msg)
		if err != nil {
			return msg, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO room_messages(id,data,room_id,sender_id,created_at) VALUES(?,?,?,?,?)",
			msg.ID, msgData, msg.RoomID, msg.SenderID, msg.CreatedAt); err != nil {
			return msg, err
		}
		for _, member := range otherMembers {
			wantReply := slices.Contains(targets, member)
			state := "handled"
			handledAt := now
			if wantReply {
				state = "pending"
				handledAt = 0
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO room_deliveries(message_id,room_id,agent_id,response_requested,state,urgent,read_at,handled_at,revision)
VALUES(?,?,?,?,?,?,0,?,1)`, msg.ID, room.ID, member, wantReply, state, msg.Urgent, handledAt); err != nil {
				return msg, err
			}
		}
		if err := s.recordRoomReplyLoop(ctx, tx, room, msg); err != nil {
			return msg, err
		}
		return msg, nil
	})
}

func (s *Store) recordRoomReplyLoop(ctx context.Context, tx *sql.Tx, room domain.MessageRoom, msg domain.RoomMessage) error {
	key := "room-reply-loop:" + room.ID
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&raw)
	type roomLoop struct {
		Version  int   `json:"version"`
		Messages int64 `json:"messages"`
		Warned   bool  `json:"warned"`
	}
	state := roomLoop{Version: 1}
	if err == nil {
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if msg.Origin == "human" {
		state.Messages = 0
		state.Warned = false
	} else {
		state.Messages++
	}
	if state.Messages >= replyLoopWarningMessages && !state.Warned {
		founder, err := activeFounder(ctx, tx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) && domain.AsFault(err).Code != domain.Denied {
			return err
		}
		if err == nil {
			id := "message_loop_" + domain.Hash(room.ID)[:32]
			warning := domain.Message{
				ID: id, ThreadID: id, SenderID: msg.SenderID, RecipientID: founder.ID,
				Kind: "direct", Origin: "service", State: "pending", Revision: 1, CreatedAt: s.timestamp(),
				Body: fmt.Sprintf("Reply-loop notice: agents in room %s have exchanged at least %d consecutive AI messages without human input.", room.ID, replyLoopWarningMessages),
			}
			if _, err := insertMessage(ctx, tx, warning); err != nil && !strings.Contains(err.Error(), "UNIQUE") {
				return err
			}
			state.Warned = true
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, data)
	return err
}

func (s *Store) RoomMessages(ctx context.Context, actor, roomID, since string, limit int) (page domain.Page[domain.RoomMessage], err error) {
	page = domain.Page[domain.RoomMessage]{Items: []domain.RoomMessage{}}
	limit, err = limitSize(limit)
	if err != nil {
		return page, err
	}
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return page, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	a, err := s.actor(ctx, snapshot, actor)
	if err != nil {
		return page, err
	}
	generation, active, err := agentOperationIdentity(ctx, snapshot, a)
	if err != nil {
		return page, err
	}
	if !active {
		return page, domain.Fail(domain.Denied, "room messages require a current active identity")
	}
	room, stored, err := loadOrSynthesizeRoom(ctx, snapshot, roomID)
	if err != nil {
		return page, err
	}
	visible, err := hydrateRoom(ctx, snapshot, actor, &room)
	if err != nil {
		return page, err
	}
	if !visible || !slices.Contains(room.MemberIDs, actor) {
		return page, domain.Fail(domain.Denied, "room messages are private to current room members")
	}
	if !stored {
		return page, nil
	}
	position := messageHistoryCursor{Version: 1, Actor: actor, Generation: generation, Query: domain.Hash("room-messages:" + room.ID)}
	if since != "" {
		position, err = s.decodeHistoryCursor(since, position)
		if err != nil {
			return page, err
		}
	} else if err := snapshot.QueryRowContext(ctx, "SELECT COALESCE(MAX(rowid),0) FROM room_messages WHERE room_id=?", room.ID).Scan(&position.Ceiling); err != nil {
		return page, err
	}
	query := "SELECT data FROM room_messages WHERE room_id=? AND rowid<=?"
	args := []any{room.ID, position.Ceiling}
	if position.ID != "" {
		query += " AND (created_at<? OR (created_at=? AND id<?))"
		args = append(args, position.Time, position.Time, position.ID)
	}
	args = append(args, limit+1)
	rows, err := snapshot.QueryContext(ctx, query+" ORDER BY created_at DESC, id DESC LIMIT ?", args...)
	if err != nil {
		return page, err
	}
	page.Items, err = scanJSON[domain.RoomMessage](rows)
	if err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		position.Time, position.ID = last.CreatedAt, last.ID
		page.NextCursor, err = s.encodeHistoryCursor(position)
		if err != nil {
			return page, err
		}
	}
	return page, nil
}

func (s *Store) RoomMessage(ctx context.Context, actor, id string) (msg domain.RoomMessage, err error) {
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return msg, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	a, err := s.actor(ctx, snapshot, actor)
	if err != nil {
		return msg, err
	}
	if _, active, err := agentOperationIdentity(ctx, snapshot, a); err != nil {
		return msg, err
	} else if !active {
		return msg, domain.Fail(domain.Denied, "room message inspection requires a current active identity")
	}
	msg, err = get[domain.RoomMessage](ctx, snapshot, "room_messages", id)
	if err != nil {
		return msg, err
	}
	room, _, err := loadOrSynthesizeRoom(ctx, snapshot, msg.RoomID)
	if err != nil {
		return msg, err
	}
	visible, err := hydrateRoom(ctx, snapshot, actor, &room)
	if err != nil {
		return msg, err
	}
	if !visible || !slices.Contains(room.MemberIDs, actor) {
		return domain.RoomMessage{}, domain.Fail(domain.Denied, "room message is private to current room members")
	}
	return msg, nil
}

func applyMessageReadReceipt(ctx context.Context, tx *sql.Tx, msg domain.RoomMessage, actor string, now int64) (domain.RoomMessage, bool, error) {
	if msg.SenderID == actor {
		return msg, false, nil
	}
	already := slices.ContainsFunc(msg.ReadBy, func(r domain.RoomReadReceipt) bool { return r.AgentID == actor })
	res, err := tx.ExecContext(ctx, "UPDATE room_deliveries SET read_at=? WHERE message_id=? AND agent_id=? AND read_at=0",
		now, msg.ID, actor)
	if err != nil {
		return msg, false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return msg, false, err
	}
	if already && affected == 0 {
		return msg, false, nil
	}
	if !already {
		msg.ReadBy = append(msg.ReadBy, domain.RoomReadReceipt{AgentID: actor, ReadAt: now})
		slices.SortFunc(msg.ReadBy, func(a, b domain.RoomReadReceipt) int {
			if a.ReadAt != b.ReadAt {
				if a.ReadAt < b.ReadAt {
					return -1
				}
				return 1
			}
			return strings.Compare(a.AgentID, b.AgentID)
		})
		msg.Revision++
		data, err := encode(msg)
		if err != nil {
			return msg, false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE room_messages SET data=? WHERE id=?", data, msg.ID); err != nil {
			return msg, false, err
		}
	}
	return msg, true, nil
}

func (s *Store) ReadRoomMessage(ctx context.Context, actor, id string, req domain.MessageReadRequest) (domain.RoomMessage, error) {
	return mutate(ctx, s, actor, "room_message.read:"+id, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.RoomMessage, error) {
		if req.ExpectedRevision != 0 {
			return domain.RoomMessage{}, domain.Fail(domain.Invalid, "read acknowledgement is independent of message revision")
		}
		msg, err := get[domain.RoomMessage](ctx, tx, "room_messages", id)
		if err != nil {
			return msg, err
		}
		room, _, err := loadOrSynthesizeRoom(ctx, tx, msg.RoomID)
		if err != nil {
			return msg, err
		}
		visible, err := hydrateRoom(ctx, tx, a.ID, &room)
		if err != nil {
			return msg, err
		}
		if !visible || !slices.Contains(room.MemberIDs, a.ID) {
			return domain.RoomMessage{}, domain.Fail(domain.Denied, "only current room members may acknowledge reading")
		}
		msg, _, err = applyMessageReadReceipt(ctx, tx, msg, a.ID, s.timestamp())
		return msg, err
	})
}

func (s *Store) ReadRoom(ctx context.Context, actor, roomID string, req domain.RoomReadRequest) (domain.RoomReadResult, error) {
	return mutate(ctx, s, actor, "room.read:"+roomID, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.RoomReadResult, error) {
		now := s.timestamp()
		result := domain.RoomReadResult{RoomID: roomID, ReadAt: now}
		room, stored, err := loadOrSynthesizeRoom(ctx, tx, roomID)
		if err != nil {
			return result, err
		}
		visible, err := hydrateRoom(ctx, tx, a.ID, &room)
		if err != nil {
			return result, err
		}
		if !visible || !slices.Contains(room.MemberIDs, a.ID) {
			return result, domain.Fail(domain.Denied, "only current room members may acknowledge reading")
		}
		if !stored {
			return result, nil
		}
		query := "SELECT m.data FROM room_messages m WHERE m.room_id=? AND m.sender_id<>?"
		args := []any{room.ID, a.ID}
		if req.UpToMessageID != "" {
			if !domain.ValidOperation(req.UpToMessageID) {
				return result, domain.Fail(domain.Invalid, "invalid up_to_message_id")
			}
			var boundRow int64
			err := tx.QueryRowContext(ctx, "SELECT rowid FROM room_messages WHERE id=? AND room_id=?", req.UpToMessageID, room.ID).Scan(&boundRow)
			if errors.Is(err, sql.ErrNoRows) {
				return result, domain.Fail(domain.NotFound, "up_to_message_id does not belong to this room")
			}
			if err != nil {
				return result, err
			}
			query += " AND m.rowid<=?"
			args = append(args, boundRow)
		}
		query += ` AND (EXISTS(SELECT 1 FROM room_deliveries d WHERE d.message_id=m.id AND d.agent_id=? AND d.read_at=0)
OR NOT EXISTS(SELECT 1 FROM json_each(m.data,'$.read_by') WHERE json_extract(value,'$.agent_id')=?)) ORDER BY m.created_at, m.id`
		args = append(args, a.ID, a.ID)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return result, err
		}
		unread, err := scanJSON[domain.RoomMessage](rows)
		if err != nil {
			return result, err
		}
		for _, m := range unread {
			_, changed, err := applyMessageReadReceipt(ctx, tx, m, a.ID, now)
			if err != nil {
				return result, err
			}
			if changed {
				result.ReadCount++
			}
		}
		return result, nil
	})
}

func (s *Store) RoomDeliveries(ctx context.Context, actor, cursor string, limit int, urgent bool) (page domain.Page[domain.RoomDelivery], err error) {
	page = domain.Page[domain.RoomDelivery]{Items: []domain.RoomDelivery{}}
	limit, err = limitSize(limit)
	if err != nil {
		return page, err
	}
	snapshot, err := s.readSnapshot(ctx)
	if err != nil {
		return page, err
	}
	defer func() { err = errors.Join(err, snapshot.Close()) }()
	if _, err := s.actor(ctx, snapshot, actor); err != nil {
		return page, err
	}
	key := "room-deliveries"
	if urgent {
		key += ":urgent"
	}
	last, err := readCursor(cursor, actor, key)
	if err != nil {
		return page, err
	}
	query := `SELECT d.message_id, d.room_id, d.state, d.response_requested, d.urgent, d.read_at, d.handled_at, d.revision, m.data
FROM room_deliveries d JOIN room_messages m ON m.id=d.message_id
WHERE d.agent_id=? AND d.state='pending' AND d.response_requested=1 AND d.message_id>?`
	args := []any{actor, last}
	if urgent {
		query += " AND d.urgent=1"
	}
	args = append(args, limit+1)
	rows, err := snapshot.QueryContext(ctx, query+" ORDER BY d.message_id LIMIT ?", args...)
	if err != nil {
		return page, err
	}
	type rawDelivery struct {
		messageID, roomID, state string
		responseRequested        bool
		urgent                   bool
		readAt, handledAt, rev   int64
		msgData                  []byte
	}
	var raws []rawDelivery
	for rows.Next() {
		var item rawDelivery
		if err := rows.Scan(&item.messageID, &item.roomID, &item.state, &item.responseRequested, &item.urgent, &item.readAt, &item.handledAt, &item.rev, &item.msgData); err != nil {
			rows.Close()
			return page, err
		}
		raws = append(raws, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return page, err
	}
	roomCache := make(map[string]domain.MessageRoom)
	for _, item := range raws {
		var msg domain.RoomMessage
		if err := json.Unmarshal(item.msgData, &msg); err != nil {
			return page, err
		}
		room, cached := roomCache[item.roomID]
		if !cached {
			var foundErr error
			room, _, foundErr = loadOrSynthesizeRoom(ctx, snapshot, item.roomID)
			if foundErr != nil {
				continue
			}
			visible, hErr := hydrateRoom(ctx, snapshot, actor, &room)
			if hErr != nil {
				return page, hErr
			}
			if !visible || !slices.Contains(room.MemberIDs, actor) {
				continue
			}
			roomCache[item.roomID] = room
		}
		page.Items = append(page.Items, domain.RoomDelivery{
			Message:         msg,
			Room:            room,
			AgentID:         actor,
			State:           item.state,
			ResponseRequest: item.responseRequested,
			Urgent:          item.urgent,
			ReadAt:          item.readAt,
			HandledAt:       item.handledAt,
			Revision:        item.rev,
		})
		if len(page.Items) > limit {
			break
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = makeCursor(actor, key, page.Items[limit-1].Message.ID)
	}
	return page, nil
}

func (s *Store) ReceiveRoomDelivery(ctx context.Context, actor, messageID string, req domain.RoomDeliveryReceipt) (domain.RoomDelivery, error) {
	return mutate(ctx, s, actor, "room_delivery.receipt:"+messageID, req.Command, req, func(tx *sql.Tx, a domain.Agent) (domain.RoomDelivery, error) {
		var delivery domain.RoomDelivery
		if !slices.Contains([]string{"handled", "stale"}, req.State) {
			return delivery, domain.Fail(domain.Invalid, "room delivery receipt state must be handled or stale")
		}
		var roomID, currentState string
		var responseRequested, urgent bool
		var readAt, handledAt, rev int64
		err := tx.QueryRowContext(ctx, `SELECT room_id, state, response_requested, urgent, read_at, handled_at, revision
FROM room_deliveries WHERE message_id=? AND agent_id=?`, messageID, a.ID).Scan(
			&roomID, &currentState, &responseRequested, &urgent, &readAt, &handledAt, &rev)
		if errors.Is(err, sql.ErrNoRows) {
			return delivery, domain.Fail(domain.NotFound, "room delivery not found for this recipient")
		}
		if err != nil {
			return delivery, err
		}
		if req.ExpectedRevision != 0 {
			if err := expect(rev, req.ExpectedRevision); err != nil {
				return delivery, err
			}
		}
		if currentState != "pending" {
			return delivery, domain.Fail(domain.Conflict, "settled room delivery cannot be reopened or overwritten")
		}
		now := s.timestamp()
		rev++
		handledAt = now
		if _, err := tx.ExecContext(ctx, "UPDATE room_deliveries SET state=?, handled_at=?, revision=? WHERE message_id=? AND agent_id=?",
			req.State, handledAt, rev, messageID, a.ID); err != nil {
			return delivery, err
		}
		msg, err := get[domain.RoomMessage](ctx, tx, "room_messages", messageID)
		if err != nil {
			return delivery, err
		}
		room, _, err := loadOrSynthesizeRoom(ctx, tx, roomID)
		if err != nil {
			return delivery, err
		}
		if _, err := hydrateRoom(ctx, tx, a.ID, &room); err != nil {
			return delivery, err
		}
		return domain.RoomDelivery{
			Message: msg, Room: room, AgentID: a.ID, State: req.State,
			ResponseRequest: responseRequested, Urgent: urgent,
			ReadAt: readAt, HandledAt: handledAt, Revision: rev,
		}, nil
	})
}
