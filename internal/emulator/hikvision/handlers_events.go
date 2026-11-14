package hikvision

// Event handlers: httpHosts (push target) and the alertStream.

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/devicelog"

	"github.com/gin-gonic/gin"
)

// HttpHostNotificationList is the payload sent by the client on
// PUT /ISAPI/Event/notification/httpHosts. We only consume the first
// HttpHostNotification entry and only the fields needed for online event
// delivery (ipAddress, portNo, url).
type HttpHostNotificationList struct {
	XMLName xml.Name                   `xml:"HttpHostNotificationList"`
	Items   []HttpHostNotificationItem `xml:"HttpHostNotification"`
}

type HttpHostNotificationItem struct {
	ID        string `xml:"id"`
	URL       string `xml:"url"`
	IPAddress string `xml:"ipAddress"`
	PortNo    string `xml:"portNo"`
	// ParameterFormatType is XML or JSON. Clients send JSON for facial
	// terminals and XML for LPR cameras; the GET must return what was stored,
	// not a fixed value.
	ParameterFormatType string `xml:"parameterFormatType"`
}

// parseHttpHostNotification extracts ipAddress, portNo, url from the raw
// XML body sent by the client. Returns an error if the body is not valid
// XML or if no HttpHostNotification entries are present.
func parseHttpHostNotification(body []byte) (HttpHostNotificationItem, error) {
	var list HttpHostNotificationList
	if err := xml.Unmarshal(body, &list); err != nil {
		return HttpHostNotificationItem{}, fmt.Errorf("invalid XML: %w", err)
	}
	if len(list.Items) == 0 {
		return HttpHostNotificationItem{}, fmt.Errorf("no HttpHostNotification entries")
	}
	return list.Items[0], nil
}

func (e *Emulator) handleGetHttpHosts(c *gin.Context) {
	e.tracer.Info("Getting Info httpHosts")

	remoteServer, _ := e.repo.GetSetting("RemoteServer")
	remotePort, _ := e.repo.GetSetting("RemotePort")
	remoteURL, _ := e.repo.GetSetting("RemoteURL")

	// The format must echo what the PUT stored. A fixed "XML" made the client
	// see a different configuration from the one it had sent.
	remoteFormat, _ := e.repo.GetSetting("RemoteFormat")
	if remoteFormat == "" {
		remoteFormat = "XML"
	}

	xmlContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<HttpHostNotificationList version="2.0" xmlns="http://www.isapi.org/ver20/XMLSchema">
    <HttpHostNotification>
        <id>1</id>
        <url>%s</url>
        <protocolType>HTTP</protocolType>
        <parameterFormatType>%s</parameterFormatType>
        <addressingFormatType>ipaddress</addressingFormatType>
        <ipAddress>%s</ipAddress>
        <portNo>%s</portNo>
        <httpAuthenticationMethod>none</httpAuthenticationMethod>
        <SubscribeEvent>
            <heartbeat>30</heartbeat>
            <eventMode>all</eventMode>
        </SubscribeEvent>
    </HttpHostNotification>
    <HttpHostNotification>
        <id>2</id>
        <url></url>
        <protocolType>EHome</protocolType>
        <parameterFormatType>XML</parameterFormatType>
        <addressingFormatType>ipaddress</addressingFormatType>
	    <ipAddress>0.0.0.0</ipAddress>
        <portNo>0</portNo>
        <httpAuthenticationMethod>none</httpAuthenticationMethod>
    </HttpHostNotification>
</HttpHostNotificationList>`, remoteURL, remoteFormat, remoteServer, remotePort)

	c.Header("Content-Type", "application/xml")
	c.String(http.StatusOK, xmlContent)
}

func (e *Emulator) handlePutHttpHosts(c *gin.Context) {
	e.tracer.Info("httpHosts: PUT received from %s, Content-Length=%d, Content-Type=%q",
		c.Request.RemoteAddr, c.Request.ContentLength, c.Request.Header.Get("Content-Type"))

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		e.tracer.Error("httpHosts: failed to read body: %v", err)
		writeHikvisionXML(c, http.StatusBadRequest, "6", "Error", "Invalid body")
		return
	}
	e.tracer.Info("httpHosts: body bytes=%d, body=%q", len(body), string(body))

	item, err := parseHttpHostNotification(body)
	if err != nil {
		e.tracer.Error("httpHosts: parse failed: %v", err)
		writeHikvisionXML(c, http.StatusBadRequest, "6", "Error", "Invalid XML")
		return
	}
	e.tracer.Info("httpHosts: parsed ipAddress=%s portNo=%s url=%s", item.IPAddress, item.PortNo, item.URL)

	if err := e.repo.SetSetting("RemoteServer", item.IPAddress); err != nil {
		e.tracer.Error("httpHosts: SetSetting RemoteServer failed: %v", err)
		writeHikvisionXML(c, http.StatusInternalServerError, "6", "Error", err.Error())
		return
	}
	if err := e.repo.SetSetting("RemotePort", item.PortNo); err != nil {
		e.tracer.Error("httpHosts: SetSetting RemotePort failed: %v", err)
		writeHikvisionXML(c, http.StatusInternalServerError, "6", "Error", err.Error())
		return
	}
	if err := e.repo.SetSetting("RemoteURL", item.URL); err != nil {
		e.tracer.Error("httpHosts: SetSetting RemoteURL failed: %v", err)
		writeHikvisionXML(c, http.StatusInternalServerError, "6", "Error", err.Error())
		return
	}

	format := strings.ToUpper(strings.TrimSpace(item.ParameterFormatType))
	if format == "" {
		format = "XML"
	}
	if err := e.repo.SetSetting("RemoteFormat", format); err != nil {
		e.tracer.Error("httpHosts: SetSetting RemoteFormat failed: %v", err)
		writeHikvisionXML(c, http.StatusInternalServerError, "6", "Error", err.Error())
		return
	}
	e.tracer.Info("httpHosts: parameterFormatType=%s persisted", format)

	e.tracer.Info("httpHosts: persisted OK, writing XML response")
	writeHikvisionXML(c, http.StatusOK, "1", "OK", "ok")
	e.tracer.Info("httpHosts: response written (status=%d, wrote=%d bytes)",
		c.Writer.Status(), c.Writer.Size())
}

func (e *Emulator) handleGetAlertStream(c *gin.Context) {
	e.tracer.Info("[GET] /alertStream")

	// Hijack the connection: the real Hikvision device sends multipart/mixed
	// without Transfer-Encoding: chunked. Go's net/http would apply chunked by
	// default and also enforce WriteTimeout, which closes the stream. Taking over
	// the raw connection reproduces the device's behavior exactly.
	hijacker, ok := c.Writer.(http.Hijacker)
	if !ok {
		e.tracer.Error("Hijacking not supported")
		c.String(http.StatusInternalServerError, "Hijacking not supported")
		return
	}

	conn, bufrw, err := hijacker.Hijack()
	if err != nil {
		e.tracer.Error("Hijack failed: %v", err)
		c.String(http.StatusInternalServerError, "Hijack failed")
		return
	}
	defer conn.Close()
	devicelog.MarkStream(c)
	remote := conn.RemoteAddr().String()
	e.dlog.StreamOpen(remote, c.Request.URL.RequestURI())
	defer e.dlog.StreamClose(remote)
	e.tracer.Info("[alertStream] connection hijacked from %s", conn.RemoteAddr())

	// Clear deadlines inherited from http.Server's ReadTimeout/WriteTimeout;
	// the stream must stay open indefinitely.
	_ = conn.SetDeadline(time.Time{})

	httpResponse := "HTTP/1.1 200 OK\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Connection: keep-alive\r\n" +
		"Content-Type: multipart/mixed; boundary=MIME_boundary\r\n" +
		"\r\n"

	n, err := bufrw.WriteString(httpResponse)
	if err != nil {
		e.tracer.Error("[alertStream] Failed to write HTTP headers: %v", err)
		return
	}
	if err := bufrw.Flush(); err != nil {
		e.tracer.Error("[alertStream] Failed to flush HTTP headers: %v", err)
		return
	}
	e.tracer.Info("[alertStream] HTTP headers sent (%d bytes)", n)

	e.handleEventStream(conn, bufrw)
	e.tracer.Info("[alertStream] event stream handler returned")
}
