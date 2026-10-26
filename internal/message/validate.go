package message

import (
	"math"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Standard protobuf permits unknown fields and last-one-wins singular fields.
// This clean protocol deliberately permits neither, including inside DTOs.
func validateProtobuf(data []byte, descriptor protoreflect.MessageDescriptor) error {
	remaining := MaxPayloadNodes
	return validateMessage(data, descriptor, 0, &remaining)
}

func validateMessage(data []byte, descriptor protoreflect.MessageDescriptor, depth int, remaining *int) error {
	if depth > MaxPayloadDepth {
		return invalid("protobuf nesting limit exceeded")
	}
	seen := make(map[protoreflect.FieldNumber]int)
	oneofs := make(map[protoreflect.FullName]bool)
	for len(data) > 0 {
		*remaining--
		if *remaining < 0 {
			return invalid("protobuf node limit exceeded")
		}
		number, wireType, n := protowire.ConsumeTag(data)
		if n < 0 {
			return invalid("malformed protobuf field")
		}
		data = data[n:]
		field := descriptor.Fields().ByNumber(number)
		if field == nil {
			return invalid("unknown protobuf field")
		}
		seen[number]++
		if !field.IsList() && seen[number] > 1 {
			return invalid("duplicate protobuf field")
		}
		if seen[number] > MaxCollectionElements {
			return invalid("protobuf collection limit exceeded")
		}
		if oneof := field.ContainingOneof(); oneof != nil {
			if oneofs[oneof.FullName()] {
				return invalid("duplicate protobuf variant")
			}
			oneofs[oneof.FullName()] = true
		}
		switch field.Kind() {
		case protoreflect.MessageKind, protoreflect.StringKind, protoreflect.BytesKind:
			if wireType != protowire.BytesType {
				return invalid("invalid protobuf wire type")
			}
			value, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return invalid("truncated protobuf field")
			}
			data = data[n:]
			if field.Kind() == protoreflect.StringKind && !utf8.Valid(value) {
				return invalid("invalid protobuf string")
			}
			if field.Kind() == protoreflect.MessageKind {
				if err := validateMessage(value, field.Message(), depth+1, remaining); err != nil {
					return err
				}
			}
		case protoreflect.BoolKind, protoreflect.EnumKind, protoreflect.Uint32Kind,
			protoreflect.Uint64Kind, protoreflect.Sint64Kind:
			if wireType != protowire.VarintType {
				return invalid("invalid protobuf wire type")
			}
			value, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return invalid("malformed protobuf integer")
			}
			data = data[n:]
			switch field.Kind() {
			case protoreflect.BoolKind:
				if value > 1 {
					return invalid("invalid protobuf boolean")
				}
			case protoreflect.Uint32Kind:
				if value > math.MaxUint32 {
					return invalid("protobuf integer overflow")
				}
			case protoreflect.EnumKind:
				if value > math.MaxInt32 || field.Enum().Values().ByNumber(protoreflect.EnumNumber(value)) == nil {
					return invalid("unknown protobuf enum")
				}
			}
		default:
			return invalid("unsupported protobuf field kind")
		}
	}
	return nil
}
