// Package blocklist embeds the deny list shipped with each release. ppgmods
// also fetches the latest copy of blocklist.json from the repository.
package blocklist

import _ "embed"

//go:embed blocklist.json
var Default []byte

const RemoteURL = "https://raw.githubusercontent.com/Trlydev/SWG/main/blocklist/blocklist.json"
