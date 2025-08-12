package handles

import (
	"fmt"
	"html"
	"net/url"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func Favicon(c *gin.Context) {
	c.Redirect(302, setting.GetStr(conf.Favicon))
}

func Robots(c *gin.Context) {
	c.String(200, setting.GetStr(conf.RobotsTxt))
}

func Plist(c *gin.Context) {
	linkNameB64 := strings.TrimSuffix(c.Param("link_name"), ".plist")
	linkName, err := utils.SafeAtob(linkNameB64)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	linkNameSplit := strings.Split(linkName, "/")
	if len(linkNameSplit) != 2 {
		common.ErrorStrResp(c, "malformed link", 400)
		return
	}
	linkEncode := linkNameSplit[0]
	linkStr, err := url.PathUnescape(linkEncode)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	link, err := url.Parse(linkStr)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	nameEncode := linkNameSplit[1]
	fullName, err := url.PathUnescape(nameEncode)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	name := fullName
	identifier := fmt.Sprintf("org.oplist.%s", fullName)
	if strings.Contains(fullName, "@") {
		ss := strings.Split(fullName, "@")
		name = strings.Join(ss[:len(ss)-1], "@")
		identifier = ss[len(ss)-1]
	}
	Url := link.String()
	Url = strings.ReplaceAll(Url, "<", "&lt;")
	Url = strings.ReplaceAll(Url, ">", "&gt;")
	name = html.EscapeString(name)
	identifier = html.EscapeString(identifier)
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
    <dict>
        <key>items</key>
        <array>
            <dict>
                <key>assets</key>
                <array>
                    <dict>
                        <key>kind</key>
                        <string>software-package</string>
                        <key>url</key>
                        <string><![CDATA[%s]]></string>
                    </dict>
                </array>
                <key>metadata</key>
                <dict>
                    <key>bundle-identifier</key>
					<string>%s</string>
					<key>bundle-version</key>
                    <string>4.4</string>
                    <key>kind</key>
                    <string>software</string>
                    <key>title</key>
                    <string>%s</string>
                </dict>
            </dict>
        </array>
    </dict>
</plist>`, Url, identifier, name)
	c.Header("Content-Type", "application/xml;charset=utf-8")
	c.Status(200)
	_, _ = c.Writer.WriteString(plist)
}
