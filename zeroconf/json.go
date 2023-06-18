package zeroconf

type GetInfoResponse struct {
	Status           int    `json:"status"`
	StatusString     string `json:"statusString"`
	SpotifyError     int    `json:"spotifyError"`
	Version          string `json:"version"`
	LibraryVersion   string `json:"libraryVersion"`
	AccountReq       string `json:"accountReq"`
	BrandDisplayName string `json:"brandDisplayName"`
	ModelDisplayName string `json:"modelDisplayName"`
	VoiceSupport     string `json:"voiceSupport"`
	Availability     string `json:"availability"`
	ProductID        int    `json:"productID"`
	TokenType        string `json:"tokenType"`
	GroupStatus      string `json:"groupStatus"`
	ResolverVersion  string `json:"resolverVersion"`
	Scope            string `json:"scope"`
	DeviceID         string `json:"deviceID"`
	RemoteName       string `json:"remoteName"`
	PublicKey        string `json:"publicKey"`
	DeviceType       string `json:"deviceType"`
	ActiveUser       string `json:"activeUser"`
}

type AddUserResponse struct {
	Status       int    `json:"status"`
	StatusString string `json:"statusString"`
	SpotifyError int    `json:"spotifyError"`
}
