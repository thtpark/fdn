package message

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thtpark/fdn/internal/domain"
	"github.com/thtpark/fdn/internal/message/payloadschema"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func mustPayload(t *testing.T, value any) []byte {
	t.Helper()
	data, err := MarshalPayload(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func requireCode(t *testing.T, err error, code domain.Code) {
	t.Helper()
	if err == nil || domain.AsFault(err).Code != code {
		t.Fatalf("got %v; want fault %s", err, code)
	}
}

func roundTrip(t *testing.T, original any) {
	t.Helper()
	data := mustPayload(t, original)
	target := reflect.New(reflect.TypeOf(original))
	if err := UnmarshalPayload(data, target.Interface()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, target.Elem().Interface()) {
		t.Fatalf("round trip differs:\ninput %#v\noutput %#v", original, target.Elem().Interface())
	}
	if again := mustPayload(t, target.Elem().Interface()); !bytes.Equal(again, data) {
		t.Fatal("protobuf encoding is not deterministic")
	}
}

func TestPayloadContractReproducible(t *testing.T) {
	contract, err := payloadschema.Contract()
	if err != nil {
		t.Fatal(err)
	}
	if contract != payloadContract {
		t.Fatal("payload.proto is stale; run go run ./tools/protocol")
	}
}

func sample(t reflect.Type, depth int) reflect.Value {
	if depth > MaxPayloadDepth {
		panic("recursive DTO fixture")
	}
	value := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.String:
		value.SetString("sample")
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int64:
		value.SetInt((1 << 60) + 123)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		value.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(9)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			value.Field(i).Set(sample(t.Field(i).Type, depth+1))
		}
	case reflect.Ptr:
		value.Set(reflect.New(t.Elem()))
		value.Elem().Set(sample(t.Elem(), depth+1))
	case reflect.Slice:
		if payloadschema.IsBytes(t) {
			value.SetBytes([]byte(`{"retained":9007199254740993}`))
		} else {
			value.Set(reflect.MakeSlice(t, 1, 1))
			value.Index(0).Set(sample(t.Elem(), depth+1))
		}
	case reflect.Map:
		value.Set(reflect.MakeMap(t))
		key := reflect.New(t.Key()).Elem()
		key.SetString("key")
		value.SetMapIndex(key, sample(t.Elem(), depth+1))
	default:
		panic("unsupported DTO fixture")
	}
	return value
}

func TestPayloadAllDomainDTOs(t *testing.T) {
	for _, typ := range payloadschema.Types {
		t.Run(typ.Name(), func(t *testing.T) {
			roundTrip(t, sample(typ, 0).Interface())
			roundTrip(t, reflect.New(typ).Elem().Interface())
		})
	}
}

func TestPayloadTransportAndUpdateDTOs(t *testing.T) {
	zero, maximum := int64(0), int64(math.MaxInt64)
	update := domain.Update{
		Sequence: math.MaxInt64, Topic: "task", SourceID: "task_1",
		TaskID: "task_1", ProjectID: "project_1", Urgent: true,
	}
	account := domain.ManagedAccount{
		AgentID: "agent_1", Username: "fixture", UID: math.MaxUint32, GID: math.MaxUint32,
		PublicKey: "test-public-key", Fingerprint: "test-fingerprint",
		PreviousFingerprints: []string{}, State: domain.AccountState("active"), Generation: math.MaxInt64,
	}
	for _, value := range []any{
		domain.LocalStatus{
			Agent:     domain.Agent{ID: "agent_local", LastSeen: math.MaxInt64},
			Identity:  domain.AgentIdentity{Generation: math.MaxInt64},
			Connected: true, Ready: true, OwnerGeneration: math.MaxInt64, Problem: domain.Unavailable,
		},
		domain.ConnectionProfile{
			Identity: domain.AgentIdentity{Generation: math.MaxInt64},
			URL:      "ssh://fixture.invalid", CA: "test-public-ca", SSHHost: "fixture.invalid",
			SSHPort: 22, KnownHosts: "test-known-host",
		},
		domain.Health{APIVersion: domain.APIVersion, SchemaVersion: domain.SchemaVersion, Version: "test"},
		domain.MessageCommand{
			Command:     domain.Command{OperationID: "message_omitted", ExpectedRevision: math.MaxInt64},
			RecipientID: "agent_1", Body: "test", Origin: "agent", Kind: "direct",
		},
		domain.MessageCommand{
			Command: domain.Command{OperationID: "message_zero", ExpectedRevision: math.MaxInt64},
			Urgent:  true, ResponseRequest: true, ExpiresAt: &zero,
		},
		domain.MessageCommand{ExpiresAt: &maximum},
		domain.RelayOpen{SessionID: "session_1", Side: "requester", Ticket: "test_ticket"},
		domain.RelayChunk{Data: []byte{0, 1, 255}, EOF: true},
		domain.RelayChunk{Data: []byte{}}, domain.RelayChunk{Data: nil},
		domain.RelayWritten{Written: 4096},
		domain.AccountState("active"), account, []domain.ManagedAccount{account},
		domain.AccountProvisionRequest{
			AgentID: "agent_1", PublicKey: "test-public-key", Generation: math.MaxInt64, Reactivate: true,
		},
		domain.AccountDisableRequest{AgentID: "agent_1", Generation: math.MaxInt64},
		update, []domain.Update{update}, []domain.Update{}, []domain.Update(nil),
		domain.Page[domain.Update]{Items: []domain.Update{update}, NextCursor: "test_cursor"},
		domain.Page[domain.Update]{Items: []domain.Update{}}, domain.Page[domain.Update]{},
	} {
		t.Run(reflect.TypeOf(value).String(), func(t *testing.T) { roundTrip(t, value) })
	}
}

func TestPayloadManagedAccountUnsignedOverflow(t *testing.T) {
	descriptor, err := payloadDescriptor(reflect.TypeFor[domain.ManagedAccount]())
	if err != nil {
		t.Fatal(err)
	}
	message := dynamicpb.NewMessage(descriptor)
	field := descriptor.Fields().ByName("uid")
	number := dynamicpb.NewMessage(field.Message())
	number.Set(number.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfUint64(uint64(math.MaxUint32)+1))
	message.Set(field, protoreflect.ValueOfMessage(number))
	data, err := proto.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	target := domain.ManagedAccount{UID: 42}
	requireCode(t, UnmarshalPayload(data, &target), domain.Invalid)
	if target.UID != 42 {
		t.Fatal("overflow partially mutated managed-account UID")
	}
}

func TestPayloadExactIntegersAndEditPresence(t *testing.T) {
	empty, zero, no := "", 0, false
	change := domain.TaskChange{
		Command: domain.Command{OperationID: "edit", ExpectedRevision: math.MaxInt64},
		Title:   &empty, Priority: &zero, Importance: &zero,
		SpecGeneration: (1 << 60) + 17, ResultGeneration: math.MaxInt64,
		HistorySequence: math.MinInt64, Status: domain.Paused,
		Policy:      &domain.ApprovalPolicy{RequiredActors: []string{}},
		RetryPolicy: &domain.RetryPolicy{}, Waiting: &domain.Waiting{RequestedIDs: []string{}},
	}
	roundTrip(t, change)
	roundTrip(t, domain.ProjectChange{Name: &empty, Archived: &no})
	roundTrip(t, domain.AgentStatusRequest{Text: &empty})
	for _, value := range []int64{math.MinInt64, math.MaxInt64, (1 << 53) + 1, 0, -1} {
		roundTrip(t, value)
	}
	roundTrip(t, uint64(math.MaxUint64))
	roundTrip(t, domain.Code(domain.Unavailable))
	roundTrip(t, domain.History{Sequence: math.MaxInt64, Snapshot: json.RawMessage(`{"preserved":9007199254740993}`)})
	input := &domain.Task{ID: "task_1", Revision: math.MaxInt64}
	var output *domain.Task
	if err := UnmarshalPayload(mustPayload(t, input), &output); err != nil || !reflect.DeepEqual(input, output) {
		t.Fatalf("root pointer: %#v, %v", output, err)
	}
}

func TestPayloadCollectionPresence(t *testing.T) {
	for _, value := range []any{
		[]domain.Task{}, []domain.Task(nil),
		domain.Page[domain.Task]{Items: []domain.Task{}}, domain.Page[domain.Task]{},
		map[string]bool{}, map[string]bool(nil), map[string]bool{"yes": true, "no": false},
		[][]byte{nil, {}, {1, 2, 3}},
		map[string][]*domain.Task{
			"nil": nil, "empty": {}, "values": {nil, {ID: "task_1", Revision: math.MaxInt64}},
		},
		map[string]*string{"nil": nil, "empty": new(string)},
		[]byte{}, []byte(nil),
	} {
		roundTrip(t, value)
	}
}

type messageInput struct {
	domain.MessageRequest
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

func TestPayloadEmbeddedExplicitShadow(t *testing.T) {
	zero := int64(0)
	input := messageInput{
		MessageRequest: domain.MessageRequest{
			Command:     domain.Command{OperationID: "message", ExpectedRevision: math.MaxInt64},
			RecipientID: "agent_1", ExpiresAt: 111,
		},
		ExpiresAt: &zero,
	}
	data := mustPayload(t, input)
	var output messageInput
	if err := UnmarshalPayload(data, &output); err != nil {
		t.Fatal(err)
	}
	if output.MessageRequest.ExpiresAt != 0 || output.ExpiresAt == nil || *output.ExpiresAt != 0 ||
		output.OperationID != "message" || output.ExpectedRevision != math.MaxInt64 {
		t.Fatalf("explicit zero did not shadow embedded field: %#v", output)
	}
	input.ExpiresAt = nil
	if err := UnmarshalPayload(mustPayload(t, input), &output); err != nil ||
		output.ExpiresAt != nil || output.MessageRequest.ExpiresAt != 0 {
		t.Fatalf("omission did not shadow embedded field: %#v, %v", output, err)
	}
	plain := domain.MessageRequest{ExpiresAt: math.MaxInt64}
	if err := UnmarshalPayload(mustPayload(t, plain), &output); err != nil ||
		output.ExpiresAt == nil || *output.ExpiresAt != math.MaxInt64 {
		t.Fatalf("compatible presence overlay: %#v, %v", output, err)
	}
	type unknownOverlay struct {
		domain.MessageRequest
		Authority bool `json:"authority"`
	}
	_, err := MarshalPayload(unknownOverlay{})
	requireCode(t, err, domain.Invalid)
	type wrongOverlay struct {
		domain.MessageRequest
		ExpiresAt *string `json:"expires_at"`
	}
	_, err = MarshalPayload(wrongOverlay{})
	requireCode(t, err, domain.Invalid)
}

func rawVarint(field protowire.Number, value uint64) []byte {
	data := protowire.AppendTag(nil, field, protowire.VarintType)
	return protowire.AppendVarint(data, value)
}

func rawBytes(field protowire.Number, value []byte) []byte {
	data := protowire.AppendTag(nil, field, protowire.BytesType)
	return protowire.AppendBytes(data, value)
}

func TestPayloadMalformedAndProtectedFields(t *testing.T) {
	valid := mustPayload(t, domain.Command{OperationID: "safe"})
	for name, data := range map[string][]byte{
		"unknown protected": append(bytes.Clone(valid), rawBytes(999, nil)...),
		"duplicate":         append(bytes.Clone(valid), valid...),
		"wrong wire type":   rawVarint(1, 1),
		"truncated":         {10, 12, 1},
		"illegal tag":       {0},
		"unterminated":      {0xff},
		"invalid UTF8":      rawBytes(1, rawBytes(1, []byte{0xff})),
		"nested unknown":    rawBytes(1, rawVarint(99, 1)),
		"missing scalar":    rawBytes(1, nil),
	} {
		t.Run(name, func(t *testing.T) {
			target := domain.Command{OperationID: "unchanged", ExpectedRevision: 55}
			err := UnmarshalPayload(data, &target)
			requireCode(t, err, domain.Invalid)
			if target.OperationID != "unchanged" || target.ExpectedRevision != 55 {
				t.Fatal("failed decode partially mutated the target")
			}
		})
	}
	var narrow int8
	requireCode(t, UnmarshalPayload(mustPayload(t, int64(128)), &narrow), domain.Invalid)
	var unsigned uint8
	requireCode(t, UnmarshalPayload(mustPayload(t, uint64(256)), &unsigned), domain.Invalid)
	var boolean bool
	requireCode(t, UnmarshalPayload(rawVarint(1, 2), &boolean), domain.Invalid)
	var integer int64
	requireCode(t, UnmarshalPayload(append([]byte{8}, bytes.Repeat([]byte{0xff}, 11)...), &integer), domain.Invalid)
	requireCode(t, UnmarshalPayload(nil, &integer), domain.Invalid)
	var list []string
	requireCode(t, UnmarshalPayload(rawBytes(1, rawBytes(1, []byte("hidden"))), &list), domain.Invalid)
}

func TestPayloadDuplicateAndIncompleteMapEntries(t *testing.T) {
	descriptor, err := payloadDescriptor(reflect.TypeFor[map[string]bool]())
	if err != nil {
		t.Fatal(err)
	}
	m := dynamicpb.NewMessage(descriptor)
	m.Set(descriptor.Fields().ByNumber(2), protoreflect.ValueOfBool(true))
	items := descriptor.Fields().ByNumber(1)
	entryDescriptor := items.Message()
	entry := dynamicpb.NewMessage(entryDescriptor)
	entry.Set(entryDescriptor.Fields().ByNumber(1), protoreflect.ValueOfString("duplicate"))
	child := dynamicpb.NewMessage(entryDescriptor.Fields().ByNumber(2).Message())
	child.Set(child.Descriptor().Fields().ByNumber(1), protoreflect.ValueOfBool(false))
	entry.Set(entryDescriptor.Fields().ByNumber(2), protoreflect.ValueOfMessage(child))
	m.Mutable(items).List().Append(protoreflect.ValueOfMessage(entry))
	m.Mutable(items).List().Append(protoreflect.ValueOfMessage(entry))
	data, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var target map[string]bool
	requireCode(t, UnmarshalPayload(data, &target), domain.Invalid)
	m.Mutable(items).List().Truncate(1)
	entry.Clear(entryDescriptor.Fields().ByNumber(1))
	data, _ = proto.Marshal(m)
	requireCode(t, UnmarshalPayload(data, &target), domain.Invalid)
}

func TestPayloadUnsupportedAndLimits(t *testing.T) {
	type recursive []recursive
	for _, value := range []any{
		nil, (*domain.Task)(nil), float64(1), float32(1), complex(1, 1), uintptr(1),
		[1]string{"array"}, make(chan int), func() {}, map[int]bool{}, map[string]any{},
		struct{ Value string }{}, time.Time{}, recursive{},
		strings.Repeat("x", MaxPayloadBytes+1), strings.Repeat("\xff", 1),
		make([]bool, MaxCollectionElements+1),
	} {
		_, err := MarshalPayload(value)
		requireCode(t, err, domain.Invalid)
	}
	for _, target := range []any{nil, domain.Command{}, (*domain.Command)(nil), new(any)} {
		requireCode(t, UnmarshalPayload(nil, target), domain.Invalid)
	}
	var target []byte
	requireCode(t, UnmarshalPayload(make([]byte, MaxPayloadBytes+1), &target), domain.Invalid)
	list := []byte{16, 1}
	for i := 0; i <= MaxCollectionElements; i++ {
		list = append(list, rawBytes(1, rawVarint(1, 0))...)
	}
	var bools []bool
	requireCode(t, UnmarshalPayload(list, &bools), domain.Invalid)
	type unknownMapKey string
	roundTrip(t, map[unknownMapKey]bool{"key": true})
}
