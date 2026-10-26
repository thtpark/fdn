package message

import (
	"bytes"
	_ "embed"
	"reflect"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/thtpark/fdn/internal/message/payloadschema"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

//go:embed payload.proto
var payloadContract string

var schemas = struct {
	sync.Mutex
	types map[reflect.Type]protoreflect.MessageDescriptor
}{types: make(map[reflect.Type]protoreflect.MessageDescriptor)}

func payloadDescriptor(t reflect.Type) (protoreflect.MessageDescriptor, error) {
	schemas.Lock()
	defer schemas.Unlock()
	if descriptor := schemas.types[t]; descriptor != nil {
		return descriptor, nil
	}
	if len(schemas.types) >= 1024 {
		return nil, invalid("protobuf schema cache limit exceeded")
	}
	descriptor, err := payloadschema.Descriptor(t)
	if err != nil {
		return nil, invalid("unsupported protobuf payload type")
	}
	schemas.types[t] = descriptor
	return descriptor, nil
}

type payloadBudget struct {
	nodes int
	bytes int
}

func (b *payloadBudget) use(depth, size int) error {
	b.nodes++
	b.bytes += size
	if depth > MaxPayloadDepth || b.nodes > MaxPayloadNodes || b.bytes > MaxPayloadBytes {
		return invalid("protobuf payload limit exceeded")
	}
	return nil
}

// MarshalPayload encodes a concrete, registered domain DTO or a supported
// primitive/container using payload.proto, never JSON, gob or google.Struct.
// Non-nil root pointers are equivalent to their values; nested pointers retain
// presence. Untyped nil, nil root pointers and interface-typed fields are errors.
func MarshalPayload(value any) ([]byte, error) {
	v := reflect.ValueOf(value)
	for v.IsValid() && v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil, invalid("nil protobuf payload")
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil, invalid("nil protobuf payload")
	}
	descriptor, err := payloadDescriptor(v.Type())
	if err != nil {
		return nil, err
	}
	m, err := encodeValue(v, descriptor, 0, &payloadBudget{})
	if err != nil {
		return nil, err
	}
	if proto.Size(m) > MaxPayloadBytes {
		return nil, invalid("protobuf payload too large")
	}
	data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(m)
	if err != nil {
		return nil, invalid("invalid protobuf payload")
	}
	return data, nil
}

// UnmarshalPayload rejects unknown fields, duplicates, overflow and malformed or
// oversized data. Assignment is atomic: an error leaves target unchanged.
func UnmarshalPayload(data []byte, target any) error {
	destination := reflect.ValueOf(target)
	if !destination.IsValid() || destination.Kind() != reflect.Ptr || destination.IsNil() {
		return invalid("protobuf target must be a non-nil pointer")
	}
	t := destination.Type().Elem()
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if len(data) > MaxPayloadBytes {
		return invalid("protobuf payload too large")
	}
	descriptor, err := payloadDescriptor(t)
	if err != nil {
		return err
	}
	if err := validateProtobuf(data, descriptor); err != nil {
		return err
	}
	m := dynamicpb.NewMessage(descriptor)
	if err := (proto.UnmarshalOptions{RecursionLimit: MaxPayloadDepth + 1}).Unmarshal(data, m); err != nil {
		return invalid("malformed protobuf payload")
	}
	value, err := decodeValue(m, t, 0, &payloadBudget{})
	if err != nil {
		return err
	}
	var wrap func(reflect.Type) reflect.Value
	wrap = func(current reflect.Type) reflect.Value {
		if current == t {
			return value
		}
		pointer := reflect.New(current.Elem())
		pointer.Elem().Set(wrap(current.Elem()))
		return pointer
	}
	destination.Elem().Set(wrap(destination.Type().Elem()))
	return nil
}

func encodeValue(v reflect.Value, descriptor protoreflect.MessageDescriptor, depth int, budget *payloadBudget) (*dynamicpb.Message, error) {
	if err := budget.use(depth, 0); err != nil {
		return nil, err
	}
	m := dynamicpb.NewMessage(descriptor)
	first := descriptor.Fields().ByNumber(1)
	switch v.Kind() {
	case reflect.String:
		if !utf8.ValidString(v.String()) {
			return nil, invalid("invalid protobuf string")
		}
		if err := budget.use(depth, len(v.String())); err != nil {
			return nil, err
		}
		m.Set(first, protoreflect.ValueOfString(v.String()))
	case reflect.Bool:
		m.Set(first, protoreflect.ValueOfBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		m.Set(first, protoreflect.ValueOfInt64(v.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		m.Set(first, protoreflect.ValueOfUint64(v.Uint()))
	case reflect.Ptr:
		if !v.IsNil() {
			child, err := encodeValue(v.Elem(), first.Message(), depth+1, budget)
			if err != nil {
				return nil, err
			}
			m.Set(first, protoreflect.ValueOfMessage(child))
		}
	case reflect.Struct:
		fields, err := payloadschema.Fields(v.Type())
		if err != nil {
			return nil, invalid("unsupported protobuf struct")
		}
		for _, field := range fields {
			value := v.FieldByIndex(field.Index)
			if value.IsZero() {
				continue
			}
			if value.Kind() == reflect.Ptr {
				value = value.Elem()
			}
			f := descriptor.Fields().ByNumber(protoreflect.FieldNumber(field.Number.ID))
			child, err := encodeValue(value, f.Message(), depth+1, budget)
			if err != nil {
				return nil, err
			}
			m.Set(f, protoreflect.ValueOfMessage(child))
		}
	case reflect.Slice:
		if payloadschema.IsBytes(v.Type()) {
			if !v.IsNil() {
				if err := budget.use(depth, v.Len()); err != nil {
					return nil, err
				}
				m.Set(first, protoreflect.ValueOfBytes(bytes.Clone(v.Bytes())))
			}
			break
		}
		if v.IsNil() {
			break
		}
		if v.Len() > MaxCollectionElements {
			return nil, invalid("protobuf collection too large")
		}
		m.Set(descriptor.Fields().ByNumber(2), protoreflect.ValueOfBool(true))
		list := m.Mutable(first).List()
		for i := 0; i < v.Len(); i++ {
			child, err := encodeValue(v.Index(i), first.Message(), depth+1, budget)
			if err != nil {
				return nil, err
			}
			list.Append(protoreflect.ValueOfMessage(child))
		}
	case reflect.Map:
		if v.IsNil() {
			break
		}
		if v.Len() > MaxCollectionElements {
			return nil, invalid("protobuf collection too large")
		}
		m.Set(descriptor.Fields().ByNumber(2), protoreflect.ValueOfBool(true))
		keys := v.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int { return bytes.Compare([]byte(a.String()), []byte(b.String())) })
		list := m.Mutable(first).List()
		keyField, valueField := first.Message().Fields().ByNumber(1), first.Message().Fields().ByNumber(2)
		for _, key := range keys {
			if !utf8.ValidString(key.String()) {
				return nil, invalid("invalid protobuf map key")
			}
			if err := budget.use(depth+1, len(key.String())); err != nil {
				return nil, err
			}
			entry := dynamicpb.NewMessage(first.Message())
			entry.Set(keyField, protoreflect.ValueOfString(key.String()))
			child, err := encodeValue(v.MapIndex(key), valueField.Message(), depth+2, budget)
			if err != nil {
				return nil, err
			}
			entry.Set(valueField, protoreflect.ValueOfMessage(child))
			list.Append(protoreflect.ValueOfMessage(entry))
		}
	default:
		return nil, invalid("unsupported protobuf payload kind")
	}
	return m, nil
}

func decodeValue(m protoreflect.Message, t reflect.Type, depth int, budget *payloadBudget) (reflect.Value, error) {
	if err := budget.use(depth, 0); err != nil {
		return reflect.Value{}, err
	}
	value := reflect.New(t).Elem()
	first := m.Descriptor().Fields().ByNumber(1)
	switch t.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !m.Has(first) {
			return reflect.Value{}, invalid("missing protobuf scalar")
		}
		switch t.Kind() {
		case reflect.String:
			value.SetString(m.Get(first).String())
		case reflect.Bool:
			value.SetBool(m.Get(first).Bool())
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n := m.Get(first).Int()
			if value.OverflowInt(n) {
				return reflect.Value{}, invalid("protobuf integer overflow")
			}
			value.SetInt(n)
		default:
			n := m.Get(first).Uint()
			if value.OverflowUint(n) {
				return reflect.Value{}, invalid("protobuf integer overflow")
			}
			value.SetUint(n)
		}
	case reflect.Ptr:
		if m.Has(first) {
			child, err := decodeValue(m.Get(first).Message(), t.Elem(), depth+1, budget)
			if err != nil {
				return reflect.Value{}, err
			}
			value.Set(reflect.New(t.Elem()))
			value.Elem().Set(child)
		}
	case reflect.Struct:
		fields, err := payloadschema.Fields(t)
		if err != nil {
			return reflect.Value{}, invalid("unsupported protobuf struct")
		}
		for _, field := range fields {
			f := m.Descriptor().Fields().ByNumber(protoreflect.FieldNumber(field.Number.ID))
			if !m.Has(f) {
				continue
			}
			fieldType := field.Type
			if fieldType.Kind() == reflect.Ptr {
				fieldType = fieldType.Elem()
			}
			child, err := decodeValue(m.Get(f).Message(), fieldType, depth+1, budget)
			if err != nil {
				return reflect.Value{}, err
			}
			if field.Type.Kind() == reflect.Ptr {
				pointer := reflect.New(fieldType)
				pointer.Elem().Set(child)
				child = pointer
			}
			value.FieldByIndex(field.Index).Set(child)
		}
	case reflect.Slice:
		if payloadschema.IsBytes(t) {
			if m.Has(first) {
				data := m.Get(first).Bytes()
				value.SetBytes(append([]byte{}, data...))
			}
			break
		}
		list := m.Get(first).List()
		present := m.Get(m.Descriptor().Fields().ByNumber(2)).Bool()
		if !present {
			if list.Len() != 0 {
				return reflect.Value{}, invalid("missing protobuf collection presence")
			}
			break
		}
		if list.Len() > MaxCollectionElements {
			return reflect.Value{}, invalid("protobuf collection too large")
		}
		value.Set(reflect.MakeSlice(t, list.Len(), list.Len()))
		for i := 0; i < list.Len(); i++ {
			child, err := decodeValue(list.Get(i).Message(), t.Elem(), depth+1, budget)
			if err != nil {
				return reflect.Value{}, err
			}
			value.Index(i).Set(child)
		}
	case reflect.Map:
		list := m.Get(first).List()
		present := m.Get(m.Descriptor().Fields().ByNumber(2)).Bool()
		if !present {
			if list.Len() != 0 {
				return reflect.Value{}, invalid("missing protobuf collection presence")
			}
			break
		}
		if list.Len() > MaxCollectionElements {
			return reflect.Value{}, invalid("protobuf collection too large")
		}
		value.Set(reflect.MakeMapWithSize(t, list.Len()))
		keyField, valueField := first.Message().Fields().ByNumber(1), first.Message().Fields().ByNumber(2)
		for i := 0; i < list.Len(); i++ {
			entry := list.Get(i).Message()
			if !entry.Has(keyField) || !entry.Has(valueField) {
				return reflect.Value{}, invalid("incomplete protobuf map entry")
			}
			key := reflect.New(t.Key()).Elem()
			key.SetString(entry.Get(keyField).String())
			if value.MapIndex(key).IsValid() {
				return reflect.Value{}, invalid("duplicate protobuf map key")
			}
			child, err := decodeValue(entry.Get(valueField).Message(), t.Elem(), depth+2, budget)
			if err != nil {
				return reflect.Value{}, err
			}
			value.SetMapIndex(key, child)
		}
	default:
		return reflect.Value{}, invalid("unsupported protobuf payload kind")
	}
	return value, nil
}
