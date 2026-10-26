// Package payloadschema supplies the explicit, versioned domain protobuf mapping.
// It does not marshal application data or make authorization decisions.
package payloadschema

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

const Package = "fdn.payload.v1"
const DomainPackage = "github.com/thtpark/fdn/internal/domain"
const MaxDepth = 32

//go:embed fields.json
var fieldJSON []byte

type Number struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

type Field struct {
	Index  []int
	Type   reflect.Type
	Number Number
}

var numbers = sync.OnceValue(func() map[string]map[string]Number {
	var out map[string]map[string]Number
	if err := json.Unmarshal(fieldJSON, &out); err != nil {
		panic("invalid checked-in protobuf field mapping")
	}
	return out
})

// Numbers returns a copy so generation cannot mutate the runtime contract.
func Numbers() map[string]map[string]Number {
	result := make(map[string]map[string]Number)
	for name, fields := range numbers() {
		result[name] = make(map[string]Number)
		for field, number := range fields {
			result[name][field] = number
		}
	}
	return result
}

func IsBytes(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8
}

func Name(t reflect.Type) (string, error) {
	return typeName(t, 0)
}

func typeName(t reflect.Type, depth int) (string, error) {
	if depth > MaxDepth {
		return "", fmt.Errorf("protobuf schema nesting limit exceeded")
	}
	switch t.Kind() {
	case reflect.String:
		return "String", nil
	case reflect.Bool:
		return "Bool", nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "Signed", nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "Unsigned", nil
	case reflect.Ptr, reflect.Slice, reflect.Map:
		if IsBytes(t) {
			return "Bytes", nil
		}
		if t.Kind() == reflect.Map && t.Key().Kind() != reflect.String {
			return "", fmt.Errorf("only string-keyed protobuf maps are supported")
		}
		child, err := typeName(t.Elem(), depth+1)
		if err != nil {
			return "", err
		}
		prefix := map[reflect.Kind]string{reflect.Ptr: "Pointer_", reflect.Slice: "List_", reflect.Map: "Map_"}[t.Kind()]
		return prefix + child, nil
	case reflect.Struct:
		if t.PkgPath() != DomainPackage {
			base, err := overlayBase(t)
			if err != nil {
				return "", err
			}
			return typeName(base, depth+1)
		}
		if strings.HasPrefix(t.Name(), "Page[") && t.NumField() == 2 &&
			t.Field(0).Name == "Items" && t.Field(0).Type.Kind() == reflect.Slice &&
			t.Field(1).Name == "NextCursor" && t.Field(1).Type.Kind() == reflect.String {
			child, err := typeName(t.Field(0).Type.Elem(), depth+1)
			return "Page_" + child, err
		}
		if _, ok := numbers()[t.Name()]; !ok {
			return "", fmt.Errorf("unregistered protobuf struct")
		}
		return "Go_" + t.Name(), nil
	default:
		return "", fmt.Errorf("unsupported protobuf kind")
	}
}

// A local operation-input overlay may shadow a registered embedded DTO field.
// It reuses that exact field ID and protobuf shape, never inventing new fields.
func overlayBase(t reflect.Type) (reflect.Type, error) {
	var base reflect.Type
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.Anonymous && field.Type.Kind() == reflect.Struct {
			if base != nil || field.Type.PkgPath() != DomainPackage || numbers()[field.Type.Name()] == nil {
				return nil, fmt.Errorf("unsupported protobuf DTO overlay")
			}
			base = field.Type
		}
	}
	if base == nil {
		return nil, fmt.Errorf("unregistered protobuf struct")
	}
	return base, nil
}

func fieldType(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Ptr {
		return t.Elem()
	}
	return t
}

// Fields flattens embedded domain DTOs using the checked-in field numbers.
// Declaration order, JSON omission options and Go map iteration never assign IDs.
func Fields(t reflect.Type) ([]Field, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("unsupported protobuf struct")
	}
	if t.PkgPath() != DomainPackage {
		base, err := overlayBase(t)
		if err != nil {
			return nil, err
		}
		fields, err := Fields(base)
		if err != nil {
			return nil, err
		}
		byName := make(map[string]int)
		baseIndex := -1
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).Anonymous {
				baseIndex = i
			}
		}
		for i := range fields {
			byName[fields[i].Number.Name] = i
			fields[i].Index = append([]int{baseIndex}, fields[i].Index...)
		}
		shadowed := make(map[string]bool)
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.Anonymous {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			index, exists := byName[name]
			if !exists || !field.IsExported() || shadowed[name] {
				return nil, fmt.Errorf("unknown or ambiguous protobuf overlay field")
			}
			expected, err := Name(fieldType(fields[index].Type))
			if err != nil {
				return nil, err
			}
			actual, err := Name(fieldType(field.Type))
			if err != nil || actual != expected {
				return nil, fmt.Errorf("protobuf overlay changes a registered field type")
			}
			fields[index].Index, fields[index].Type = []int{i}, field.Type
			shadowed[name] = true
		}
		return fields, nil
	}
	if _, err := Name(t); err != nil {
		return nil, err
	}
	mapping := numbers()[t.Name()]
	if strings.HasPrefix(t.Name(), "Page[") {
		mapping = map[string]Number{"Items": {1, "items"}, "NextCursor": {2, "next_cursor"}}
	}
	var result []Field
	seen := make(map[int32]bool)
	var visit func(reflect.Type, []int) error
	visit = func(current reflect.Type, path []int) error {
		for i := 0; i < current.NumField(); i++ {
			field := current.Field(i)
			index := append(slices.Clone(path), i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				if err := visit(field.Type, index); err != nil {
					return err
				}
				continue
			}
			number, ok := mapping[field.Name]
			if !field.IsExported() || !ok || number.ID < 1 || number.ID > (1<<29)-1 ||
				(number.ID >= 19000 && number.ID <= 19999) || seen[number.ID] {
				return fmt.Errorf("domain fields do not match the protobuf contract")
			}
			if _, err := Name(field.Type); err != nil {
				return err
			}
			if tag := strings.Split(field.Tag.Get("json"), ",")[0]; tag != number.Name {
				return fmt.Errorf("domain field name does not match the protobuf contract")
			}
			seen[number.ID] = true
			result = append(result, Field{Index: index, Type: field.Type, Number: number})
		}
		return nil
	}
	if err := visit(t, nil); err != nil {
		return nil, err
	}
	slices.SortFunc(result, func(a, b Field) int { return int(a.Number.ID - b.Number.ID) })
	return result, nil
}

type builder struct {
	messages map[string]*descriptorpb.DescriptorProto
}

func scalar(name string, kind descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto {
	return &descriptorpb.FieldDescriptorProto{
		Name: proto.String(name), Number: proto.Int32(1), Type: kind.Enum(),
		Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
	}
}

func optional(field *descriptorpb.FieldDescriptorProto, message *descriptorpb.DescriptorProto) {
	field.Proto3Optional = proto.Bool(true)
	field.OneofIndex = proto.Int32(int32(len(message.OneofDecl)))
	message.OneofDecl = append(message.OneofDecl, &descriptorpb.OneofDescriptorProto{Name: proto.String("_" + field.GetName())})
}

func messageField(name string, id int32, typeName string, repeated bool) *descriptorpb.FieldDescriptorProto {
	label := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	if repeated {
		label = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	}
	return &descriptorpb.FieldDescriptorProto{
		Name: proto.String(name), Number: proto.Int32(id), Label: label.Enum(),
		Type: descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(), TypeName: proto.String("." + Package + "." + typeName),
	}
}

func (b *builder) add(t reflect.Type, depth int) (string, error) {
	if depth > MaxDepth {
		return "", fmt.Errorf("protobuf schema nesting limit exceeded")
	}
	name, err := Name(t)
	if err != nil {
		return "", err
	}
	if _, exists := b.messages[name]; exists {
		return name, nil
	}
	m := &descriptorpb.DescriptorProto{Name: proto.String(name)}
	b.messages[name] = m
	var primitive descriptorpb.FieldDescriptorProto_Type
	switch t.Kind() {
	case reflect.String:
		primitive = descriptorpb.FieldDescriptorProto_TYPE_STRING
	case reflect.Bool:
		primitive = descriptorpb.FieldDescriptorProto_TYPE_BOOL
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		primitive = descriptorpb.FieldDescriptorProto_TYPE_SINT64
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		primitive = descriptorpb.FieldDescriptorProto_TYPE_UINT64
	case reflect.Slice:
		if IsBytes(t) {
			primitive = descriptorpb.FieldDescriptorProto_TYPE_BYTES
		}
	}
	if primitive != 0 {
		f := scalar("value", primitive)
		optional(f, m)
		m.Field = append(m.Field, f)
		return name, nil
	}
	switch t.Kind() {
	case reflect.Struct:
		fields, err := Fields(t)
		if err != nil {
			return "", err
		}
		used := make(map[int32]bool)
		for _, field := range fields {
			child, err := b.add(fieldType(field.Type), depth+1)
			if err != nil {
				return "", err
			}
			m.Field = append(m.Field, messageField(field.Number.Name, field.Number.ID, child, false))
			used[field.Number.ID] = true
		}
		for _, number := range numbers()[t.Name()] {
			if !used[number.ID] {
				m.ReservedRange = append(m.ReservedRange, &descriptorpb.DescriptorProto_ReservedRange{
					Start: proto.Int32(number.ID), End: proto.Int32(number.ID + 1),
				})
				m.ReservedName = append(m.ReservedName, number.Name)
			}
		}
		slices.SortFunc(m.ReservedRange, func(a, b *descriptorpb.DescriptorProto_ReservedRange) int { return int(a.GetStart() - b.GetStart()) })
		slices.Sort(m.ReservedName)
	case reflect.Ptr:
		child, err := b.add(t.Elem(), depth+1)
		if err != nil {
			return "", err
		}
		m.Field = append(m.Field, messageField("value", 1, child, false))
	case reflect.Slice, reflect.Map:
		child, err := b.add(t.Elem(), depth+1)
		if err != nil {
			return "", err
		}
		if t.Kind() == reflect.Map {
			entry := &descriptorpb.DescriptorProto{Name: proto.String(name + "_Entry")}
			key := scalar("key", descriptorpb.FieldDescriptorProto_TYPE_STRING)
			optional(key, entry)
			entry.Field = append(entry.Field, key, messageField("value", 2, child, false))
			b.messages[entry.GetName()] = entry
			child = entry.GetName()
		}
		m.Field = append(m.Field, messageField("items", 1, child, true))
		present := scalar("present", descriptorpb.FieldDescriptorProto_TYPE_BOOL)
		present.Number = proto.Int32(2)
		m.Field = append(m.Field, present)
	default:
		return "", fmt.Errorf("unsupported protobuf kind")
	}
	return name, nil
}

func build(types []reflect.Type) (*descriptorpb.FileDescriptorProto, error) {
	b := &builder{messages: make(map[string]*descriptorpb.DescriptorProto)}
	for _, t := range types {
		if _, err := b.add(t, 0); err != nil {
			return nil, err
		}
	}
	file := &descriptorpb.FileDescriptorProto{
		Name: proto.String("fdn_payload.proto"), Package: proto.String(Package), Syntax: proto.String("proto3"),
	}
	for _, m := range b.messages {
		file.MessageType = append(file.MessageType, m)
	}
	slices.SortFunc(file.MessageType, func(a, b *descriptorpb.DescriptorProto) int { return strings.Compare(a.GetName(), b.GetName()) })
	return file, nil
}

func Descriptor(t reflect.Type) (protoreflect.MessageDescriptor, error) {
	file, err := build([]reflect.Type{t})
	if err != nil {
		return nil, err
	}
	descriptor, err := protodesc.NewFile(file, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid protobuf schema: %w", err)
	}
	name, _ := Name(t)
	return descriptor.Messages().ByName(protoreflect.Name(name)), nil
}

// Contract renders deterministic, directly compilable protobuf source. The
// field-number ledger, rather than Go declaration order, is the compatibility lock.
func Contract() (string, error) {
	types := slices.Clone(Types)
	types = append(types, reflect.TypeFor[string](), reflect.TypeFor[bool](), reflect.TypeFor[int64](),
		reflect.TypeFor[uint64](), reflect.TypeFor[[]byte]())
	for _, t := range slices.Clone(types) {
		types = append(types, reflect.SliceOf(t), reflect.MapOf(reflect.TypeFor[string](), t))
	}
	file, err := build(types)
	if err != nil {
		return "", err
	}
	if _, err := protodesc.NewFile(file, nil); err != nil {
		return "", err
	}
	var text strings.Builder
	text.WriteString("// Code generated by tools/protocol; DO NOT EDIT.\n")
	text.WriteString("// Explicit IDs are locked in payloadschema/fields.json. See README.md.\n")
	text.WriteString("syntax = \"proto3\";\n\npackage " + Package + ";\n\n")
	for _, m := range file.MessageType {
		text.WriteString("message " + m.GetName() + " {\n")
		for _, r := range m.ReservedRange {
			text.WriteString("  reserved " + strconv.Itoa(int(r.GetStart())) + ";\n")
		}
		for _, n := range m.ReservedName {
			text.WriteString("  reserved " + strconv.Quote(n) + ";\n")
		}
		for _, f := range m.Field {
			prefix := ""
			if f.GetLabel() == descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
				prefix = "repeated "
			} else if f.GetProto3Optional() {
				prefix = "optional "
			}
			kind := strings.ToLower(strings.TrimPrefix(f.GetType().String(), "TYPE_"))
			if f.GetType() == descriptorpb.FieldDescriptorProto_TYPE_MESSAGE {
				kind = strings.TrimPrefix(f.GetTypeName(), "."+Package+".")
			}
			text.WriteString("  " + prefix + kind + " " + f.GetName() + " = " + strconv.Itoa(int(f.GetNumber())) + ";\n")
		}
		text.WriteString("}\n\n")
	}
	return text.String(), nil
}
