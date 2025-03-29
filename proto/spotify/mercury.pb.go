// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        (unknown)
// source: spotify/mercury.proto

package spotify

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type MercuryReply_CachePolicy int32

const (
	MercuryReply_CACHE_NO      MercuryReply_CachePolicy = 1
	MercuryReply_CACHE_PRIVATE MercuryReply_CachePolicy = 2
	MercuryReply_CACHE_PUBLIC  MercuryReply_CachePolicy = 3
)

// Enum value maps for MercuryReply_CachePolicy.
var (
	MercuryReply_CachePolicy_name = map[int32]string{
		1: "CACHE_NO",
		2: "CACHE_PRIVATE",
		3: "CACHE_PUBLIC",
	}
	MercuryReply_CachePolicy_value = map[string]int32{
		"CACHE_NO":      1,
		"CACHE_PRIVATE": 2,
		"CACHE_PUBLIC":  3,
	}
)

func (x MercuryReply_CachePolicy) Enum() *MercuryReply_CachePolicy {
	p := new(MercuryReply_CachePolicy)
	*p = x
	return p
}

func (x MercuryReply_CachePolicy) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (MercuryReply_CachePolicy) Descriptor() protoreflect.EnumDescriptor {
	return file_spotify_mercury_proto_enumTypes[0].Descriptor()
}

func (MercuryReply_CachePolicy) Type() protoreflect.EnumType {
	return &file_spotify_mercury_proto_enumTypes[0]
}

func (x MercuryReply_CachePolicy) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Do not use.
func (x *MercuryReply_CachePolicy) UnmarshalJSON(b []byte) error {
	num, err := protoimpl.X.UnmarshalJSONEnum(x.Descriptor(), b)
	if err != nil {
		return err
	}
	*x = MercuryReply_CachePolicy(num)
	return nil
}

// Deprecated: Use MercuryReply_CachePolicy.Descriptor instead.
func (MercuryReply_CachePolicy) EnumDescriptor() ([]byte, []int) {
	return file_spotify_mercury_proto_rawDescGZIP(), []int{3, 0}
}

type MercuryMultiGetRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Request       []*MercuryRequest      `protobuf:"bytes,1,rep,name=request" json:"request,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *MercuryMultiGetRequest) Reset() {
	*x = MercuryMultiGetRequest{}
	mi := &file_spotify_mercury_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *MercuryMultiGetRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*MercuryMultiGetRequest) ProtoMessage() {}

func (x *MercuryMultiGetRequest) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_mercury_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use MercuryMultiGetRequest.ProtoReflect.Descriptor instead.
func (*MercuryMultiGetRequest) Descriptor() ([]byte, []int) {
	return file_spotify_mercury_proto_rawDescGZIP(), []int{0}
}

func (x *MercuryMultiGetRequest) GetRequest() []*MercuryRequest {
	if x != nil {
		return x.Request
	}
	return nil
}

type MercuryMultiGetReply struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Reply         []*MercuryReply        `protobuf:"bytes,1,rep,name=reply" json:"reply,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *MercuryMultiGetReply) Reset() {
	*x = MercuryMultiGetReply{}
	mi := &file_spotify_mercury_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *MercuryMultiGetReply) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*MercuryMultiGetReply) ProtoMessage() {}

func (x *MercuryMultiGetReply) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_mercury_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use MercuryMultiGetReply.ProtoReflect.Descriptor instead.
func (*MercuryMultiGetReply) Descriptor() ([]byte, []int) {
	return file_spotify_mercury_proto_rawDescGZIP(), []int{1}
}

func (x *MercuryMultiGetReply) GetReply() []*MercuryReply {
	if x != nil {
		return x.Reply
	}
	return nil
}

type MercuryRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Uri           *string                `protobuf:"bytes,1,opt,name=uri" json:"uri,omitempty"`
	ContentType   *string                `protobuf:"bytes,2,opt,name=content_type,json=contentType" json:"content_type,omitempty"`
	Body          []byte                 `protobuf:"bytes,3,opt,name=body" json:"body,omitempty"`
	Etag          []byte                 `protobuf:"bytes,4,opt,name=etag" json:"etag,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *MercuryRequest) Reset() {
	*x = MercuryRequest{}
	mi := &file_spotify_mercury_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *MercuryRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*MercuryRequest) ProtoMessage() {}

func (x *MercuryRequest) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_mercury_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use MercuryRequest.ProtoReflect.Descriptor instead.
func (*MercuryRequest) Descriptor() ([]byte, []int) {
	return file_spotify_mercury_proto_rawDescGZIP(), []int{2}
}

func (x *MercuryRequest) GetUri() string {
	if x != nil && x.Uri != nil {
		return *x.Uri
	}
	return ""
}

func (x *MercuryRequest) GetContentType() string {
	if x != nil && x.ContentType != nil {
		return *x.ContentType
	}
	return ""
}

func (x *MercuryRequest) GetBody() []byte {
	if x != nil {
		return x.Body
	}
	return nil
}

func (x *MercuryRequest) GetEtag() []byte {
	if x != nil {
		return x.Etag
	}
	return nil
}

type MercuryReply struct {
	state         protoimpl.MessageState    `protogen:"open.v1"`
	StatusCode    *int32                    `protobuf:"zigzag32,1,opt,name=status_code,json=statusCode" json:"status_code,omitempty"`
	StatusMessage *string                   `protobuf:"bytes,2,opt,name=status_message,json=statusMessage" json:"status_message,omitempty"`
	CachePolicy   *MercuryReply_CachePolicy `protobuf:"varint,3,opt,name=cache_policy,json=cachePolicy,enum=spotify.MercuryReply_CachePolicy" json:"cache_policy,omitempty"`
	Ttl           *int32                    `protobuf:"zigzag32,4,opt,name=ttl" json:"ttl,omitempty"`
	Etag          []byte                    `protobuf:"bytes,5,opt,name=etag" json:"etag,omitempty"`
	ContentType   *string                   `protobuf:"bytes,6,opt,name=content_type,json=contentType" json:"content_type,omitempty"`
	Body          []byte                    `protobuf:"bytes,7,opt,name=body" json:"body,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *MercuryReply) Reset() {
	*x = MercuryReply{}
	mi := &file_spotify_mercury_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *MercuryReply) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*MercuryReply) ProtoMessage() {}

func (x *MercuryReply) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_mercury_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use MercuryReply.ProtoReflect.Descriptor instead.
func (*MercuryReply) Descriptor() ([]byte, []int) {
	return file_spotify_mercury_proto_rawDescGZIP(), []int{3}
}

func (x *MercuryReply) GetStatusCode() int32 {
	if x != nil && x.StatusCode != nil {
		return *x.StatusCode
	}
	return 0
}

func (x *MercuryReply) GetStatusMessage() string {
	if x != nil && x.StatusMessage != nil {
		return *x.StatusMessage
	}
	return ""
}

func (x *MercuryReply) GetCachePolicy() MercuryReply_CachePolicy {
	if x != nil && x.CachePolicy != nil {
		return *x.CachePolicy
	}
	return MercuryReply_CACHE_NO
}

func (x *MercuryReply) GetTtl() int32 {
	if x != nil && x.Ttl != nil {
		return *x.Ttl
	}
	return 0
}

func (x *MercuryReply) GetEtag() []byte {
	if x != nil {
		return x.Etag
	}
	return nil
}

func (x *MercuryReply) GetContentType() string {
	if x != nil && x.ContentType != nil {
		return *x.ContentType
	}
	return ""
}

func (x *MercuryReply) GetBody() []byte {
	if x != nil {
		return x.Body
	}
	return nil
}

type MercuryHeader struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Uri           *string                `protobuf:"bytes,1,opt,name=uri" json:"uri,omitempty"`
	ContentType   *string                `protobuf:"bytes,2,opt,name=content_type,json=contentType" json:"content_type,omitempty"`
	Method        *string                `protobuf:"bytes,3,opt,name=method" json:"method,omitempty"`
	StatusCode    *int32                 `protobuf:"zigzag32,4,opt,name=status_code,json=statusCode" json:"status_code,omitempty"`
	UserFields    []*MercuryUserField    `protobuf:"bytes,6,rep,name=user_fields,json=userFields" json:"user_fields,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *MercuryHeader) Reset() {
	*x = MercuryHeader{}
	mi := &file_spotify_mercury_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *MercuryHeader) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*MercuryHeader) ProtoMessage() {}

func (x *MercuryHeader) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_mercury_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use MercuryHeader.ProtoReflect.Descriptor instead.
func (*MercuryHeader) Descriptor() ([]byte, []int) {
	return file_spotify_mercury_proto_rawDescGZIP(), []int{4}
}

func (x *MercuryHeader) GetUri() string {
	if x != nil && x.Uri != nil {
		return *x.Uri
	}
	return ""
}

func (x *MercuryHeader) GetContentType() string {
	if x != nil && x.ContentType != nil {
		return *x.ContentType
	}
	return ""
}

func (x *MercuryHeader) GetMethod() string {
	if x != nil && x.Method != nil {
		return *x.Method
	}
	return ""
}

func (x *MercuryHeader) GetStatusCode() int32 {
	if x != nil && x.StatusCode != nil {
		return *x.StatusCode
	}
	return 0
}

func (x *MercuryHeader) GetUserFields() []*MercuryUserField {
	if x != nil {
		return x.UserFields
	}
	return nil
}

type MercuryUserField struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Key           *string                `protobuf:"bytes,1,opt,name=key" json:"key,omitempty"`
	Value         []byte                 `protobuf:"bytes,2,opt,name=value" json:"value,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *MercuryUserField) Reset() {
	*x = MercuryUserField{}
	mi := &file_spotify_mercury_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *MercuryUserField) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*MercuryUserField) ProtoMessage() {}

func (x *MercuryUserField) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_mercury_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use MercuryUserField.ProtoReflect.Descriptor instead.
func (*MercuryUserField) Descriptor() ([]byte, []int) {
	return file_spotify_mercury_proto_rawDescGZIP(), []int{5}
}

func (x *MercuryUserField) GetKey() string {
	if x != nil && x.Key != nil {
		return *x.Key
	}
	return ""
}

func (x *MercuryUserField) GetValue() []byte {
	if x != nil {
		return x.Value
	}
	return nil
}

var File_spotify_mercury_proto protoreflect.FileDescriptor

const file_spotify_mercury_proto_rawDesc = "" +
	"\n" +
	"\x15spotify/mercury.proto\x12\aspotify\"K\n" +
	"\x16MercuryMultiGetRequest\x121\n" +
	"\arequest\x18\x01 \x03(\v2\x17.spotify.MercuryRequestR\arequest\"C\n" +
	"\x14MercuryMultiGetReply\x12+\n" +
	"\x05reply\x18\x01 \x03(\v2\x15.spotify.MercuryReplyR\x05reply\"m\n" +
	"\x0eMercuryRequest\x12\x10\n" +
	"\x03uri\x18\x01 \x01(\tR\x03uri\x12!\n" +
	"\fcontent_type\x18\x02 \x01(\tR\vcontentType\x12\x12\n" +
	"\x04body\x18\x03 \x01(\fR\x04body\x12\x12\n" +
	"\x04etag\x18\x04 \x01(\fR\x04etag\"\xbb\x02\n" +
	"\fMercuryReply\x12\x1f\n" +
	"\vstatus_code\x18\x01 \x01(\x11R\n" +
	"statusCode\x12%\n" +
	"\x0estatus_message\x18\x02 \x01(\tR\rstatusMessage\x12D\n" +
	"\fcache_policy\x18\x03 \x01(\x0e2!.spotify.MercuryReply.CachePolicyR\vcachePolicy\x12\x10\n" +
	"\x03ttl\x18\x04 \x01(\x11R\x03ttl\x12\x12\n" +
	"\x04etag\x18\x05 \x01(\fR\x04etag\x12!\n" +
	"\fcontent_type\x18\x06 \x01(\tR\vcontentType\x12\x12\n" +
	"\x04body\x18\a \x01(\fR\x04body\"@\n" +
	"\vCachePolicy\x12\f\n" +
	"\bCACHE_NO\x10\x01\x12\x11\n" +
	"\rCACHE_PRIVATE\x10\x02\x12\x10\n" +
	"\fCACHE_PUBLIC\x10\x03\"\xb9\x01\n" +
	"\rMercuryHeader\x12\x10\n" +
	"\x03uri\x18\x01 \x01(\tR\x03uri\x12!\n" +
	"\fcontent_type\x18\x02 \x01(\tR\vcontentType\x12\x16\n" +
	"\x06method\x18\x03 \x01(\tR\x06method\x12\x1f\n" +
	"\vstatus_code\x18\x04 \x01(\x11R\n" +
	"statusCode\x12:\n" +
	"\vuser_fields\x18\x06 \x03(\v2\x19.spotify.MercuryUserFieldR\n" +
	"userFields\":\n" +
	"\x10MercuryUserField\x12\x10\n" +
	"\x03key\x18\x01 \x01(\tR\x03key\x12\x14\n" +
	"\x05value\x18\x02 \x01(\fR\x05valueB\x88\x01\n" +
	"\vcom.spotifyB\fMercuryProtoP\x01Z/github.com/devgianlu/go-librespot/proto/spotify\xa2\x02\x03SXX\xaa\x02\aSpotify\xca\x02\aSpotify\xe2\x02\x13Spotify\\GPBMetadata\xea\x02\aSpotify"

var (
	file_spotify_mercury_proto_rawDescOnce sync.Once
	file_spotify_mercury_proto_rawDescData []byte
)

func file_spotify_mercury_proto_rawDescGZIP() []byte {
	file_spotify_mercury_proto_rawDescOnce.Do(func() {
		file_spotify_mercury_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_spotify_mercury_proto_rawDesc), len(file_spotify_mercury_proto_rawDesc)))
	})
	return file_spotify_mercury_proto_rawDescData
}

var file_spotify_mercury_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_spotify_mercury_proto_msgTypes = make([]protoimpl.MessageInfo, 6)
var file_spotify_mercury_proto_goTypes = []any{
	(MercuryReply_CachePolicy)(0),  // 0: spotify.MercuryReply.CachePolicy
	(*MercuryMultiGetRequest)(nil), // 1: spotify.MercuryMultiGetRequest
	(*MercuryMultiGetReply)(nil),   // 2: spotify.MercuryMultiGetReply
	(*MercuryRequest)(nil),         // 3: spotify.MercuryRequest
	(*MercuryReply)(nil),           // 4: spotify.MercuryReply
	(*MercuryHeader)(nil),          // 5: spotify.MercuryHeader
	(*MercuryUserField)(nil),       // 6: spotify.MercuryUserField
}
var file_spotify_mercury_proto_depIdxs = []int32{
	3, // 0: spotify.MercuryMultiGetRequest.request:type_name -> spotify.MercuryRequest
	4, // 1: spotify.MercuryMultiGetReply.reply:type_name -> spotify.MercuryReply
	0, // 2: spotify.MercuryReply.cache_policy:type_name -> spotify.MercuryReply.CachePolicy
	6, // 3: spotify.MercuryHeader.user_fields:type_name -> spotify.MercuryUserField
	4, // [4:4] is the sub-list for method output_type
	4, // [4:4] is the sub-list for method input_type
	4, // [4:4] is the sub-list for extension type_name
	4, // [4:4] is the sub-list for extension extendee
	0, // [0:4] is the sub-list for field type_name
}

func init() { file_spotify_mercury_proto_init() }
func file_spotify_mercury_proto_init() {
	if File_spotify_mercury_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_spotify_mercury_proto_rawDesc), len(file_spotify_mercury_proto_rawDesc)),
			NumEnums:      1,
			NumMessages:   6,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_spotify_mercury_proto_goTypes,
		DependencyIndexes: file_spotify_mercury_proto_depIdxs,
		EnumInfos:         file_spotify_mercury_proto_enumTypes,
		MessageInfos:      file_spotify_mercury_proto_msgTypes,
	}.Build()
	File_spotify_mercury_proto = out.File
	file_spotify_mercury_proto_goTypes = nil
	file_spotify_mercury_proto_depIdxs = nil
}
