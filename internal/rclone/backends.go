package rclone

import (
	"github.com/rclone/rclone/fs"

	// The backends lagra offers: those it can open from option values alone
	// (addresses, user names, passwords and keys). lagra opens backends
	// without running rclone's interactive config, so it leaves out backends
	// that need a browser sign-in (OAuth: Google Drive, OneDrive, Dropbox,
	// Box, pCloud, ...) or a login step that stores tokens (iCloud Drive,
	// Proton Drive, Internxt, PikPak, SugarSync), and wrappers around another
	// remote (crypt, union, alias, ...), as lagra has no remotes for them to
	// name. A backend imported here is offered everywhere: settings window,
	// `lagra backends` and validation.
	_ "github.com/rclone/rclone/backend/azureblob"
	_ "github.com/rclone/rclone/backend/azurefiles"
	_ "github.com/rclone/rclone/backend/b2"
	_ "github.com/rclone/rclone/backend/cloudinary"
	_ "github.com/rclone/rclone/backend/doi"
	_ "github.com/rclone/rclone/backend/drime"
	_ "github.com/rclone/rclone/backend/fichier"
	_ "github.com/rclone/rclone/backend/filefabric"
	_ "github.com/rclone/rclone/backend/filelu"
	_ "github.com/rclone/rclone/backend/filen"
	_ "github.com/rclone/rclone/backend/filescom"
	_ "github.com/rclone/rclone/backend/ftp"
	_ "github.com/rclone/rclone/backend/gofile"
	_ "github.com/rclone/rclone/backend/hdfs"
	_ "github.com/rclone/rclone/backend/http"
	_ "github.com/rclone/rclone/backend/imagekit"
	_ "github.com/rclone/rclone/backend/internetarchive"
	_ "github.com/rclone/rclone/backend/koofr"
	_ "github.com/rclone/rclone/backend/linkbox"
	_ "github.com/rclone/rclone/backend/mailru"
	_ "github.com/rclone/rclone/backend/mega"
	_ "github.com/rclone/rclone/backend/netstorage"
	_ "github.com/rclone/rclone/backend/opendrive"
	_ "github.com/rclone/rclone/backend/oracleobjectstorage"
	_ "github.com/rclone/rclone/backend/pixeldrain"
	_ "github.com/rclone/rclone/backend/qingstor"
	_ "github.com/rclone/rclone/backend/quatrix"
	_ "github.com/rclone/rclone/backend/s3"
	_ "github.com/rclone/rclone/backend/seafile"
	_ "github.com/rclone/rclone/backend/sftp"
	_ "github.com/rclone/rclone/backend/shade"
	_ "github.com/rclone/rclone/backend/sia"
	_ "github.com/rclone/rclone/backend/smb"
	_ "github.com/rclone/rclone/backend/storj"
	_ "github.com/rclone/rclone/backend/swift"
	_ "github.com/rclone/rclone/backend/ulozto"
	_ "github.com/rclone/rclone/backend/webdav"

	// Not offered, see offered.
	_ "github.com/rclone/rclone/backend/local"
)

// offered reports whether lagra offers a registered backend. rclone
// registers two more on its own, which lagra must not offer: local, which
// the VFS cache keeps its files through, and crypt, which rclone's
// operations package imports. Hidden aliases are not offered either.
func offered(ri *fs.RegInfo) bool {
	return !ri.Hide && ri.Name != "local" && ri.Name != "crypt"
}
