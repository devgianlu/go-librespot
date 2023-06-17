// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.30.0
// 	protoc        v4.23.2
// source: login5_identifiers.proto

package identifiers

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type PhoneNumber struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Number             string `protobuf:"bytes,1,opt,name=number,proto3" json:"number,omitempty"`
	IsoCountryCode     string `protobuf:"bytes,2,opt,name=iso_country_code,json=isoCountryCode,proto3" json:"iso_country_code,omitempty"`
	CountryCallingCode string `protobuf:"bytes,3,opt,name=country_calling_code,json=countryCallingCode,proto3" json:"country_calling_code,omitempty"`
}

func (x *PhoneNumber) Reset() {
	*x = PhoneNumber{}
	if protoimpl.UnsafeEnabled {
		mi := &file_login5_identifiers_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *PhoneNumber) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*PhoneNumber) ProtoMessage() {}

func (x *PhoneNumber) ProtoReflect() protoreflect.Message {
	mi := &file_login5_identifiers_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use PhoneNumber.ProtoReflect.Descriptor instead.
func (*PhoneNumber) Descriptor() ([]byte, []int) {
	return file_login5_identifiers_proto_rawDescGZIP(), []int{0}
}

func (x *PhoneNumber) GetNumber() string {
	if x != nil {
		return x.Number
	}
	return ""
}

func (x *PhoneNumber) GetIsoCountryCode() string {
	if x != nil {
		return x.IsoCountryCode
	}
	return ""
}

func (x *PhoneNumber) GetCountryCallingCode() string {
	if x != nil {
		return x.CountryCallingCode
	}
	return ""
}

var File_login5_identifiers_proto protoreflect.FileDescriptor

var file_login5_identifiers_proto_rawDesc = []byte{
	0x0a, 0x18, 0x6c, 0x6f, 0x67, 0x69, 0x6e, 0x35, 0x5f, 0x69, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x66,
	0x69, 0x65, 0x72, 0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x1d, 0x73, 0x70, 0x6f, 0x74,
	0x69, 0x66, 0x79, 0x2e, 0x6c, 0x6f, 0x67, 0x69, 0x6e, 0x35, 0x2e, 0x76, 0x33, 0x2e, 0x69, 0x64,
	0x65, 0x6e, 0x74, 0x69, 0x66, 0x69, 0x65, 0x72, 0x73, 0x22, 0x81, 0x01, 0x0a, 0x0b, 0x50, 0x68,
	0x6f, 0x6e, 0x65, 0x4e, 0x75, 0x6d, 0x62, 0x65, 0x72, 0x12, 0x16, 0x0a, 0x06, 0x6e, 0x75, 0x6d,
	0x62, 0x65, 0x72, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x06, 0x6e, 0x75, 0x6d, 0x62, 0x65,
	0x72, 0x12, 0x28, 0x0a, 0x10, 0x69, 0x73, 0x6f, 0x5f, 0x63, 0x6f, 0x75, 0x6e, 0x74, 0x72, 0x79,
	0x5f, 0x63, 0x6f, 0x64, 0x65, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0e, 0x69, 0x73, 0x6f,
	0x43, 0x6f, 0x75, 0x6e, 0x74, 0x72, 0x79, 0x43, 0x6f, 0x64, 0x65, 0x12, 0x30, 0x0a, 0x14, 0x63,
	0x6f, 0x75, 0x6e, 0x74, 0x72, 0x79, 0x5f, 0x63, 0x61, 0x6c, 0x6c, 0x69, 0x6e, 0x67, 0x5f, 0x63,
	0x6f, 0x64, 0x65, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52, 0x12, 0x63, 0x6f, 0x75, 0x6e, 0x74,
	0x72, 0x79, 0x43, 0x61, 0x6c, 0x6c, 0x69, 0x6e, 0x67, 0x43, 0x6f, 0x64, 0x65, 0x42, 0x32, 0x5a,
	0x30, 0x67, 0x6f, 0x2d, 0x6c, 0x69, 0x62, 0x72, 0x65, 0x73, 0x70, 0x6f, 0x74, 0x2f, 0x70, 0x72,
	0x6f, 0x74, 0x6f, 0x2f, 0x73, 0x70, 0x6f, 0x74, 0x69, 0x66, 0x79, 0x2f, 0x6c, 0x6f, 0x67, 0x69,
	0x6e, 0x35, 0x2f, 0x76, 0x33, 0x2f, 0x69, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x66, 0x69, 0x65, 0x72,
	0x73, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_login5_identifiers_proto_rawDescOnce sync.Once
	file_login5_identifiers_proto_rawDescData = file_login5_identifiers_proto_rawDesc
)

func file_login5_identifiers_proto_rawDescGZIP() []byte {
	file_login5_identifiers_proto_rawDescOnce.Do(func() {
		file_login5_identifiers_proto_rawDescData = protoimpl.X.CompressGZIP(file_login5_identifiers_proto_rawDescData)
	})
	return file_login5_identifiers_proto_rawDescData
}

var file_login5_identifiers_proto_msgTypes = make([]protoimpl.MessageInfo, 1)
var file_login5_identifiers_proto_goTypes = []interface{}{
	(*PhoneNumber)(nil), // 0: spotify.login5.v3.identifiers.PhoneNumber
}
var file_login5_identifiers_proto_depIdxs = []int32{
	0, // [0:0] is the sub-list for method output_type
	0, // [0:0] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_login5_identifiers_proto_init() }
func file_login5_identifiers_proto_init() {
	if File_login5_identifiers_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_login5_identifiers_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*PhoneNumber); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_login5_identifiers_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   1,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_login5_identifiers_proto_goTypes,
		DependencyIndexes: file_login5_identifiers_proto_depIdxs,
		MessageInfos:      file_login5_identifiers_proto_msgTypes,
	}.Build()
	File_login5_identifiers_proto = out.File
	file_login5_identifiers_proto_rawDesc = nil
	file_login5_identifiers_proto_goTypes = nil
	file_login5_identifiers_proto_depIdxs = nil
}