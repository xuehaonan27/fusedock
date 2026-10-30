package baidu

import (
	"fmt"
	"path"
	"time"

	"github.com/xuehaonan27/fusedock/pkg/provider"
)

type meta struct {
	ID       string
	ParentID string
	Name     string
	Path     string
	Size     int64
	MD5      string
	Modified time.Time
	Dir      bool
}

func (m meta) object() provider.Object {
	kind, size := provider.KindFile, m.Size
	if m.Dir {
		kind, size = provider.KindDirectory, 0
	}
	version := fmt.Sprintf("%d:%d:%s", m.Modified.Unix(), size, m.MD5)
	digest := provider.Digest{Algorithm: "md5", Value: m.MD5}
	contentVersion := ""
	if digest.Valid() && !m.Dir {
		contentVersion = version
	} else {
		digest = provider.Digest{}
	}
	return provider.Object{
		ID: m.ID, ParentID: m.ParentID, Name: m.Name, Kind: kind, Size: size,
		Version: version, MetadataVersion: version, ContentVersion: contentVersion, Digest: digest, Modified: m.Modified,
	}
}

type savedState struct {
	Version       int    `json:"version"`
	APIEndpoint   string `json:"api_endpoint"`
	OAuthEndpoint string `json:"oauth_endpoint"`
	AppKey        string `json:"app_key"`
	Username      string `json:"username"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	ExpiresAt     int64  `json:"expires_at"`
}

type listResponse struct {
	Errno int        `json:"errno"`
	List  []fileInfo `json:"list"`
}

type fileInfo struct {
	FsID           int64  `json:"fs_id"`
	ServerFilename string `json:"server_filename"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	MD5            string `json:"md5"`
	IsDir          int    `json:"isdir"`
	ServerMtime    int64  `json:"server_mtime"`
}

func (f fileInfo) meta(parent string) meta {
	name := f.ServerFilename
	if name == "" {
		name = path.Base(f.Path)
	}
	return meta{
		ID: fmt.Sprint(f.FsID), ParentID: parent, Name: name, Path: f.Path,
		Size: f.Size, MD5: f.MD5, Modified: time.Unix(f.ServerMtime, 0), Dir: f.IsDir == 1,
	}
}

type downloadResponse struct {
	Errno int `json:"errno"`
	List  []struct {
		Dlink string `json:"dlink"`
	} `json:"list"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	QRCodeURL       string `json:"qrcode_url"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
	Error           string `json:"error"`
	Description     string `json:"error_description"`
}

type userInfoResponse struct {
	OpenID   string `json:"openid"`
	Username string `json:"username"`
	Error    string `json:"error"`
	ErrorMsg string `json:"error_msg"`
}
