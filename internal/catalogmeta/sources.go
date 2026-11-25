package catalogmeta

const (
	PrimarySourceID = "api"
	UdonHost        = "api.udon.dance"
	UdonURL         = "https://" + UdonHost + "/Api/Songs/list"
	UdonHTTPURL     = "http://" + UdonHost + "/Api/Songs/list"
	KivaURL         = "https://x.kiva.moe/api/v2/wanna/songs"
	WannaURL        = "https://wanna.kiva.moe/api/wannaInfo"
)

var sources = [...]struct{ id, url string }{
	{PrimarySourceID, UdonURL},
	{"api-http", UdonHTTPURL},
	{"kiva", KivaURL},
	{"wanna", WannaURL},
}

// SourceIDs returns the catalog order without exposing mutable definitions.
func SourceIDs() []string {
	ids := make([]string, len(sources))
	for i, source := range sources {
		ids[i] = source.id
	}
	return ids
}

// SourceURL preserves Udon as the default catalog for unspecified routes.
func SourceURL(id string) string {
	for _, source := range sources {
		if source.id == id {
			return source.url
		}
	}
	return UdonURL
}
