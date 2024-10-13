// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.35.1
// 	protoc        (unknown)
// source: spotify/login5/v3/credentials/login5_credentials.proto

package credentials

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

type StoredCredential struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Username string `protobuf:"bytes,1,opt,name=username,proto3" json:"username,omitempty"`
	Data     []byte `protobuf:"bytes,2,opt,name=data,proto3" json:"data,omitempty"`
}

func (x *StoredCredential) Reset() {
	*x = StoredCredential{}
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *StoredCredential) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*StoredCredential) ProtoMessage() {}

func (x *StoredCredential) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use StoredCredential.ProtoReflect.Descriptor instead.
func (*StoredCredential) Descriptor() ([]byte, []int) {
	return file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescGZIP(), []int{0}
}

func (x *StoredCredential) GetUsername() string {
	if x != nil {
		return x.Username
	}
	return ""
}

func (x *StoredCredential) GetData() []byte {
	if x != nil {
		return x.Data
	}
	return nil
}

type Password struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Id       string `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
	Password string `protobuf:"bytes,2,opt,name=password,proto3" json:"password,omitempty"`
	Padding  []byte `protobuf:"bytes,3,opt,name=padding,proto3" json:"padding,omitempty"`
}

func (x *Password) Reset() {
	*x = Password{}
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Password) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Password) ProtoMessage() {}

func (x *Password) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Password.ProtoReflect.Descriptor instead.
func (*Password) Descriptor() ([]byte, []int) {
	return file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescGZIP(), []int{1}
}

func (x *Password) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *Password) GetPassword() string {
	if x != nil {
		return x.Password
	}
	return ""
}

func (x *Password) GetPadding() []byte {
	if x != nil {
		return x.Padding
	}
	return nil
}

type FacebookAccessToken struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	FbUid       string `protobuf:"bytes,1,opt,name=fb_uid,json=fbUid,proto3" json:"fb_uid,omitempty"`
	AccessToken string `protobuf:"bytes,2,opt,name=access_token,json=accessToken,proto3" json:"access_token,omitempty"`
}

func (x *FacebookAccessToken) Reset() {
	*x = FacebookAccessToken{}
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *FacebookAccessToken) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*FacebookAccessToken) ProtoMessage() {}

func (x *FacebookAccessToken) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use FacebookAccessToken.ProtoReflect.Descriptor instead.
func (*FacebookAccessToken) Descriptor() ([]byte, []int) {
	return file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescGZIP(), []int{2}
}

func (x *FacebookAccessToken) GetFbUid() string {
	if x != nil {
		return x.FbUid
	}
	return ""
}

func (x *FacebookAccessToken) GetAccessToken() string {
	if x != nil {
		return x.AccessToken
	}
	return ""
}

type OneTimeToken struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Token string `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
}

func (x *OneTimeToken) Reset() {
	*x = OneTimeToken{}
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *OneTimeToken) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*OneTimeToken) ProtoMessage() {}

func (x *OneTimeToken) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use OneTimeToken.ProtoReflect.Descriptor instead.
func (*OneTimeToken) Descriptor() ([]byte, []int) {
	return file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescGZIP(), []int{3}
}

func (x *OneTimeToken) GetToken() string {
	if x != nil {
		return x.Token
	}
	return ""
}

type ParentChildCredential struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	ChildId                string            `protobuf:"bytes,1,opt,name=child_id,json=childId,proto3" json:"child_id,omitempty"`
	ParentStoredCredential *StoredCredential `protobuf:"bytes,2,opt,name=parent_stored_credential,json=parentStoredCredential,proto3" json:"parent_stored_credential,omitempty"`
}

func (x *ParentChildCredential) Reset() {
	*x = ParentChildCredential{}
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ParentChildCredential) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ParentChildCredential) ProtoMessage() {}

func (x *ParentChildCredential) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ParentChildCredential.ProtoReflect.Descriptor instead.
func (*ParentChildCredential) Descriptor() ([]byte, []int) {
	return file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescGZIP(), []int{4}
}

func (x *ParentChildCredential) GetChildId() string {
	if x != nil {
		return x.ChildId
	}
	return ""
}

func (x *ParentChildCredential) GetParentStoredCredential() *StoredCredential {
	if x != nil {
		return x.ParentStoredCredential
	}
	return nil
}

type AppleSignInCredential struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	AuthCode    string `protobuf:"bytes,1,opt,name=auth_code,json=authCode,proto3" json:"auth_code,omitempty"`
	RedirectUri string `protobuf:"bytes,2,opt,name=redirect_uri,json=redirectUri,proto3" json:"redirect_uri,omitempty"`
	BundleId    string `protobuf:"bytes,3,opt,name=bundle_id,json=bundleId,proto3" json:"bundle_id,omitempty"`
}

func (x *AppleSignInCredential) Reset() {
	*x = AppleSignInCredential{}
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AppleSignInCredential) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AppleSignInCredential) ProtoMessage() {}

func (x *AppleSignInCredential) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AppleSignInCredential.ProtoReflect.Descriptor instead.
func (*AppleSignInCredential) Descriptor() ([]byte, []int) {
	return file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescGZIP(), []int{5}
}

func (x *AppleSignInCredential) GetAuthCode() string {
	if x != nil {
		return x.AuthCode
	}
	return ""
}

func (x *AppleSignInCredential) GetRedirectUri() string {
	if x != nil {
		return x.RedirectUri
	}
	return ""
}

func (x *AppleSignInCredential) GetBundleId() string {
	if x != nil {
		return x.BundleId
	}
	return ""
}

type SamsungSignInCredential struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	AuthCode         string `protobuf:"bytes,1,opt,name=auth_code,json=authCode,proto3" json:"auth_code,omitempty"`
	RedirectUri      string `protobuf:"bytes,2,opt,name=redirect_uri,json=redirectUri,proto3" json:"redirect_uri,omitempty"`
	IdToken          string `protobuf:"bytes,3,opt,name=id_token,json=idToken,proto3" json:"id_token,omitempty"`
	TokenEndpointUrl string `protobuf:"bytes,4,opt,name=token_endpoint_url,json=tokenEndpointUrl,proto3" json:"token_endpoint_url,omitempty"`
}

func (x *SamsungSignInCredential) Reset() {
	*x = SamsungSignInCredential{}
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SamsungSignInCredential) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SamsungSignInCredential) ProtoMessage() {}

func (x *SamsungSignInCredential) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SamsungSignInCredential.ProtoReflect.Descriptor instead.
func (*SamsungSignInCredential) Descriptor() ([]byte, []int) {
	return file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescGZIP(), []int{6}
}

func (x *SamsungSignInCredential) GetAuthCode() string {
	if x != nil {
		return x.AuthCode
	}
	return ""
}

func (x *SamsungSignInCredential) GetRedirectUri() string {
	if x != nil {
		return x.RedirectUri
	}
	return ""
}

func (x *SamsungSignInCredential) GetIdToken() string {
	if x != nil {
		return x.IdToken
	}
	return ""
}

func (x *SamsungSignInCredential) GetTokenEndpointUrl() string {
	if x != nil {
		return x.TokenEndpointUrl
	}
	return ""
}

type GoogleSignInCredential struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	AuthCode    string `protobuf:"bytes,1,opt,name=auth_code,json=authCode,proto3" json:"auth_code,omitempty"`
	RedirectUri string `protobuf:"bytes,2,opt,name=redirect_uri,json=redirectUri,proto3" json:"redirect_uri,omitempty"`
}

func (x *GoogleSignInCredential) Reset() {
	*x = GoogleSignInCredential{}
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GoogleSignInCredential) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GoogleSignInCredential) ProtoMessage() {}

func (x *GoogleSignInCredential) ProtoReflect() protoreflect.Message {
	mi := &file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GoogleSignInCredential.ProtoReflect.Descriptor instead.
func (*GoogleSignInCredential) Descriptor() ([]byte, []int) {
	return file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescGZIP(), []int{7}
}

func (x *GoogleSignInCredential) GetAuthCode() string {
	if x != nil {
		return x.AuthCode
	}
	return ""
}

func (x *GoogleSignInCredential) GetRedirectUri() string {
	if x != nil {
		return x.RedirectUri
	}
	return ""
}

var File_spotify_login5_v3_credentials_login5_credentials_proto protoreflect.FileDescriptor

var file_spotify_login5_v3_credentials_login5_credentials_proto_rawDesc = []byte{
	0x0a, 0x36, 0x73, 0x70, 0x6f, 0x74, 0x69, 0x66, 0x79, 0x2f, 0x6c, 0x6f, 0x67, 0x69, 0x6e, 0x35,
	0x2f, 0x76, 0x33, 0x2f, 0x63, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x73, 0x2f,
	0x6c, 0x6f, 0x67, 0x69, 0x6e, 0x35, 0x5f, 0x63, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61,
	0x6c, 0x73, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x1d, 0x73, 0x70, 0x6f, 0x74, 0x69, 0x66,
	0x79, 0x2e, 0x6c, 0x6f, 0x67, 0x69, 0x6e, 0x35, 0x2e, 0x76, 0x33, 0x2e, 0x63, 0x72, 0x65, 0x64,
	0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x73, 0x22, 0x42, 0x0a, 0x10, 0x53, 0x74, 0x6f, 0x72, 0x65,
	0x64, 0x43, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x12, 0x1a, 0x0a, 0x08, 0x75,
	0x73, 0x65, 0x72, 0x6e, 0x61, 0x6d, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x75,
	0x73, 0x65, 0x72, 0x6e, 0x61, 0x6d, 0x65, 0x12, 0x12, 0x0a, 0x04, 0x64, 0x61, 0x74, 0x61, 0x18,
	0x02, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x04, 0x64, 0x61, 0x74, 0x61, 0x22, 0x50, 0x0a, 0x08, 0x50,
	0x61, 0x73, 0x73, 0x77, 0x6f, 0x72, 0x64, 0x12, 0x0e, 0x0a, 0x02, 0x69, 0x64, 0x18, 0x01, 0x20,
	0x01, 0x28, 0x09, 0x52, 0x02, 0x69, 0x64, 0x12, 0x1a, 0x0a, 0x08, 0x70, 0x61, 0x73, 0x73, 0x77,
	0x6f, 0x72, 0x64, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x70, 0x61, 0x73, 0x73, 0x77,
	0x6f, 0x72, 0x64, 0x12, 0x18, 0x0a, 0x07, 0x70, 0x61, 0x64, 0x64, 0x69, 0x6e, 0x67, 0x18, 0x03,
	0x20, 0x01, 0x28, 0x0c, 0x52, 0x07, 0x70, 0x61, 0x64, 0x64, 0x69, 0x6e, 0x67, 0x22, 0x4f, 0x0a,
	0x13, 0x46, 0x61, 0x63, 0x65, 0x62, 0x6f, 0x6f, 0x6b, 0x41, 0x63, 0x63, 0x65, 0x73, 0x73, 0x54,
	0x6f, 0x6b, 0x65, 0x6e, 0x12, 0x15, 0x0a, 0x06, 0x66, 0x62, 0x5f, 0x75, 0x69, 0x64, 0x18, 0x01,
	0x20, 0x01, 0x28, 0x09, 0x52, 0x05, 0x66, 0x62, 0x55, 0x69, 0x64, 0x12, 0x21, 0x0a, 0x0c, 0x61,
	0x63, 0x63, 0x65, 0x73, 0x73, 0x5f, 0x74, 0x6f, 0x6b, 0x65, 0x6e, 0x18, 0x02, 0x20, 0x01, 0x28,
	0x09, 0x52, 0x0b, 0x61, 0x63, 0x63, 0x65, 0x73, 0x73, 0x54, 0x6f, 0x6b, 0x65, 0x6e, 0x22, 0x24,
	0x0a, 0x0c, 0x4f, 0x6e, 0x65, 0x54, 0x69, 0x6d, 0x65, 0x54, 0x6f, 0x6b, 0x65, 0x6e, 0x12, 0x14,
	0x0a, 0x05, 0x74, 0x6f, 0x6b, 0x65, 0x6e, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x05, 0x74,
	0x6f, 0x6b, 0x65, 0x6e, 0x22, 0x9d, 0x01, 0x0a, 0x15, 0x50, 0x61, 0x72, 0x65, 0x6e, 0x74, 0x43,
	0x68, 0x69, 0x6c, 0x64, 0x43, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x12, 0x19,
	0x0a, 0x08, 0x63, 0x68, 0x69, 0x6c, 0x64, 0x5f, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09,
	0x52, 0x07, 0x63, 0x68, 0x69, 0x6c, 0x64, 0x49, 0x64, 0x12, 0x69, 0x0a, 0x18, 0x70, 0x61, 0x72,
	0x65, 0x6e, 0x74, 0x5f, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x64, 0x5f, 0x63, 0x72, 0x65, 0x64, 0x65,
	0x6e, 0x74, 0x69, 0x61, 0x6c, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0b, 0x32, 0x2f, 0x2e, 0x73, 0x70,
	0x6f, 0x74, 0x69, 0x66, 0x79, 0x2e, 0x6c, 0x6f, 0x67, 0x69, 0x6e, 0x35, 0x2e, 0x76, 0x33, 0x2e,
	0x63, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x73, 0x2e, 0x53, 0x74, 0x6f, 0x72,
	0x65, 0x64, 0x43, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x52, 0x16, 0x70, 0x61,
	0x72, 0x65, 0x6e, 0x74, 0x53, 0x74, 0x6f, 0x72, 0x65, 0x64, 0x43, 0x72, 0x65, 0x64, 0x65, 0x6e,
	0x74, 0x69, 0x61, 0x6c, 0x22, 0x74, 0x0a, 0x15, 0x41, 0x70, 0x70, 0x6c, 0x65, 0x53, 0x69, 0x67,
	0x6e, 0x49, 0x6e, 0x43, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x12, 0x1b, 0x0a,
	0x09, 0x61, 0x75, 0x74, 0x68, 0x5f, 0x63, 0x6f, 0x64, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09,
	0x52, 0x08, 0x61, 0x75, 0x74, 0x68, 0x43, 0x6f, 0x64, 0x65, 0x12, 0x21, 0x0a, 0x0c, 0x72, 0x65,
	0x64, 0x69, 0x72, 0x65, 0x63, 0x74, 0x5f, 0x75, 0x72, 0x69, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09,
	0x52, 0x0b, 0x72, 0x65, 0x64, 0x69, 0x72, 0x65, 0x63, 0x74, 0x55, 0x72, 0x69, 0x12, 0x1b, 0x0a,
	0x09, 0x62, 0x75, 0x6e, 0x64, 0x6c, 0x65, 0x5f, 0x69, 0x64, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09,
	0x52, 0x08, 0x62, 0x75, 0x6e, 0x64, 0x6c, 0x65, 0x49, 0x64, 0x22, 0xa2, 0x01, 0x0a, 0x17, 0x53,
	0x61, 0x6d, 0x73, 0x75, 0x6e, 0x67, 0x53, 0x69, 0x67, 0x6e, 0x49, 0x6e, 0x43, 0x72, 0x65, 0x64,
	0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x12, 0x1b, 0x0a, 0x09, 0x61, 0x75, 0x74, 0x68, 0x5f, 0x63,
	0x6f, 0x64, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x61, 0x75, 0x74, 0x68, 0x43,
	0x6f, 0x64, 0x65, 0x12, 0x21, 0x0a, 0x0c, 0x72, 0x65, 0x64, 0x69, 0x72, 0x65, 0x63, 0x74, 0x5f,
	0x75, 0x72, 0x69, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0b, 0x72, 0x65, 0x64, 0x69, 0x72,
	0x65, 0x63, 0x74, 0x55, 0x72, 0x69, 0x12, 0x19, 0x0a, 0x08, 0x69, 0x64, 0x5f, 0x74, 0x6f, 0x6b,
	0x65, 0x6e, 0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52, 0x07, 0x69, 0x64, 0x54, 0x6f, 0x6b, 0x65,
	0x6e, 0x12, 0x2c, 0x0a, 0x12, 0x74, 0x6f, 0x6b, 0x65, 0x6e, 0x5f, 0x65, 0x6e, 0x64, 0x70, 0x6f,
	0x69, 0x6e, 0x74, 0x5f, 0x75, 0x72, 0x6c, 0x18, 0x04, 0x20, 0x01, 0x28, 0x09, 0x52, 0x10, 0x74,
	0x6f, 0x6b, 0x65, 0x6e, 0x45, 0x6e, 0x64, 0x70, 0x6f, 0x69, 0x6e, 0x74, 0x55, 0x72, 0x6c, 0x22,
	0x58, 0x0a, 0x16, 0x47, 0x6f, 0x6f, 0x67, 0x6c, 0x65, 0x53, 0x69, 0x67, 0x6e, 0x49, 0x6e, 0x43,
	0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x12, 0x1b, 0x0a, 0x09, 0x61, 0x75, 0x74,
	0x68, 0x5f, 0x63, 0x6f, 0x64, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x08, 0x61, 0x75,
	0x74, 0x68, 0x43, 0x6f, 0x64, 0x65, 0x12, 0x21, 0x0a, 0x0c, 0x72, 0x65, 0x64, 0x69, 0x72, 0x65,
	0x63, 0x74, 0x5f, 0x75, 0x72, 0x69, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x0b, 0x72, 0x65,
	0x64, 0x69, 0x72, 0x65, 0x63, 0x74, 0x55, 0x72, 0x69, 0x42, 0x9a, 0x02, 0x0a, 0x21, 0x63, 0x6f,
	0x6d, 0x2e, 0x73, 0x70, 0x6f, 0x74, 0x69, 0x66, 0x79, 0x2e, 0x6c, 0x6f, 0x67, 0x69, 0x6e, 0x35,
	0x2e, 0x76, 0x33, 0x2e, 0x63, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x73, 0x42,
	0x16, 0x4c, 0x6f, 0x67, 0x69, 0x6e, 0x35, 0x43, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61,
	0x6c, 0x73, 0x50, 0x72, 0x6f, 0x74, 0x6f, 0x50, 0x01, 0x5a, 0x45, 0x67, 0x69, 0x74, 0x68, 0x75,
	0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x64, 0x65, 0x76, 0x67, 0x69, 0x61, 0x6e, 0x6c, 0x75, 0x2f,
	0x67, 0x6f, 0x2d, 0x6c, 0x69, 0x62, 0x72, 0x65, 0x73, 0x70, 0x6f, 0x74, 0x2f, 0x70, 0x72, 0x6f,
	0x74, 0x6f, 0x2f, 0x73, 0x70, 0x6f, 0x74, 0x69, 0x66, 0x79, 0x2f, 0x6c, 0x6f, 0x67, 0x69, 0x6e,
	0x35, 0x2f, 0x76, 0x33, 0x2f, 0x63, 0x72, 0x65, 0x64, 0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x73,
	0xa2, 0x02, 0x04, 0x53, 0x4c, 0x56, 0x43, 0xaa, 0x02, 0x1d, 0x53, 0x70, 0x6f, 0x74, 0x69, 0x66,
	0x79, 0x2e, 0x4c, 0x6f, 0x67, 0x69, 0x6e, 0x35, 0x2e, 0x56, 0x33, 0x2e, 0x43, 0x72, 0x65, 0x64,
	0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x73, 0xca, 0x02, 0x1d, 0x53, 0x70, 0x6f, 0x74, 0x69, 0x66,
	0x79, 0x5c, 0x4c, 0x6f, 0x67, 0x69, 0x6e, 0x35, 0x5c, 0x56, 0x33, 0x5c, 0x43, 0x72, 0x65, 0x64,
	0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x73, 0xe2, 0x02, 0x29, 0x53, 0x70, 0x6f, 0x74, 0x69, 0x66,
	0x79, 0x5c, 0x4c, 0x6f, 0x67, 0x69, 0x6e, 0x35, 0x5c, 0x56, 0x33, 0x5c, 0x43, 0x72, 0x65, 0x64,
	0x65, 0x6e, 0x74, 0x69, 0x61, 0x6c, 0x73, 0x5c, 0x47, 0x50, 0x42, 0x4d, 0x65, 0x74, 0x61, 0x64,
	0x61, 0x74, 0x61, 0xea, 0x02, 0x20, 0x53, 0x70, 0x6f, 0x74, 0x69, 0x66, 0x79, 0x3a, 0x3a, 0x4c,
	0x6f, 0x67, 0x69, 0x6e, 0x35, 0x3a, 0x3a, 0x56, 0x33, 0x3a, 0x3a, 0x43, 0x72, 0x65, 0x64, 0x65,
	0x6e, 0x74, 0x69, 0x61, 0x6c, 0x73, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescOnce sync.Once
	file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescData = file_spotify_login5_v3_credentials_login5_credentials_proto_rawDesc
)

func file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescGZIP() []byte {
	file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescOnce.Do(func() {
		file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescData = protoimpl.X.CompressGZIP(file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescData)
	})
	return file_spotify_login5_v3_credentials_login5_credentials_proto_rawDescData
}

var file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes = make([]protoimpl.MessageInfo, 8)
var file_spotify_login5_v3_credentials_login5_credentials_proto_goTypes = []any{
	(*StoredCredential)(nil),        // 0: spotify.login5.v3.credentials.StoredCredential
	(*Password)(nil),                // 1: spotify.login5.v3.credentials.Password
	(*FacebookAccessToken)(nil),     // 2: spotify.login5.v3.credentials.FacebookAccessToken
	(*OneTimeToken)(nil),            // 3: spotify.login5.v3.credentials.OneTimeToken
	(*ParentChildCredential)(nil),   // 4: spotify.login5.v3.credentials.ParentChildCredential
	(*AppleSignInCredential)(nil),   // 5: spotify.login5.v3.credentials.AppleSignInCredential
	(*SamsungSignInCredential)(nil), // 6: spotify.login5.v3.credentials.SamsungSignInCredential
	(*GoogleSignInCredential)(nil),  // 7: spotify.login5.v3.credentials.GoogleSignInCredential
}
var file_spotify_login5_v3_credentials_login5_credentials_proto_depIdxs = []int32{
	0, // 0: spotify.login5.v3.credentials.ParentChildCredential.parent_stored_credential:type_name -> spotify.login5.v3.credentials.StoredCredential
	1, // [1:1] is the sub-list for method output_type
	1, // [1:1] is the sub-list for method input_type
	1, // [1:1] is the sub-list for extension type_name
	1, // [1:1] is the sub-list for extension extendee
	0, // [0:1] is the sub-list for field type_name
}

func init() { file_spotify_login5_v3_credentials_login5_credentials_proto_init() }
func file_spotify_login5_v3_credentials_login5_credentials_proto_init() {
	if File_spotify_login5_v3_credentials_login5_credentials_proto != nil {
		return
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_spotify_login5_v3_credentials_login5_credentials_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   8,
			NumExtensions: 0,
			NumServices:   0,
		},
		GoTypes:           file_spotify_login5_v3_credentials_login5_credentials_proto_goTypes,
		DependencyIndexes: file_spotify_login5_v3_credentials_login5_credentials_proto_depIdxs,
		MessageInfos:      file_spotify_login5_v3_credentials_login5_credentials_proto_msgTypes,
	}.Build()
	File_spotify_login5_v3_credentials_login5_credentials_proto = out.File
	file_spotify_login5_v3_credentials_login5_credentials_proto_rawDesc = nil
	file_spotify_login5_v3_credentials_login5_credentials_proto_goTypes = nil
	file_spotify_login5_v3_credentials_login5_credentials_proto_depIdxs = nil
}
