//go:build go1.21

// Package dlna provides DLNA server.
package dlna

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/internal/conf"
	"github.com/OpenListTeam/OpenList/internal/fs"
	"github.com/OpenListTeam/OpenList/internal/model"
	"github.com/OpenListTeam/OpenList/pkg/utils"
	"github.com/OpenListTeam/OpenList/server/common"
	"github.com/OpenListTeam/OpenList/server/dlna/data"
	"github.com/OpenListTeam/OpenList/server/dlna/dlnaflags"
	dms_dlna "github.com/anacrolix/dms/dlna"
	"github.com/anacrolix/dms/soap"
	"github.com/anacrolix/dms/ssdp"
	"github.com/anacrolix/dms/upnp"
	alog "github.com/anacrolix/log"
	log "github.com/sirupsen/logrus"
)

func Run(opt *dlnaflags.Options) error {
	s, err := newServer(opt)
	if err != nil {
		return err
	}
	if err := s.Serve(); err != nil {
		return err
	}
	s.Wait()
	return nil
}

const (
	serverField       = "Linux/3.4 DLNADOC/1.50 UPnP/1.0 DMS/1.0"
	rootDescPath      = "/rootDesc.xml"
	resPath           = "/r/"
	serviceControlURL = "/ctl"
)

type server struct {
	// The service SOAP handler keyed by service URN.
	services map[string]UPnPService

	Interfaces []net.Interface

	HTTPConn       net.Listener
	httpListenAddr string
	handler        http.Handler

	RootDeviceUUID string

	FriendlyName string

	// For waiting on the listener to close
	waitChan chan struct{}

	// Time interval between SSPD announces
	AnnounceInterval time.Duration

	RootDir string
}

func newServer(opt *dlnaflags.Options) (*server, error) {
	friendlyName := opt.FriendlyName
	if friendlyName == "" {
		friendlyName = makeDefaultFriendlyName()
	}
	interfaces := make([]net.Interface, 0, len(opt.InterfaceNames))
	for _, interfaceName := range opt.InterfaceNames {
		var err error
		intf, err := net.InterfaceByName(interfaceName)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve interface name '%s': %w", interfaceName, err)
		}
		if !isAppropriatelyConfigured(*intf) {
			return nil, fmt.Errorf("interface '%s' is not appropriately configured (it should be UP, MULTICAST and MTU > 0)", interfaceName)
		}
		interfaces = append(interfaces, *intf)
	}
	if len(interfaces) == 0 {
		interfaces = listInterfaces()
	}

	s := &server{
		AnnounceInterval: opt.AnnounceInterval,
		FriendlyName:     friendlyName,
		RootDeviceUUID:   makeDeviceUUID(friendlyName),
		Interfaces:       interfaces,
		waitChan:         make(chan struct{}),
		httpListenAddr:   opt.ListenAddr,
		RootDir:          opt.RootDir,
	}

	s.services = map[string]UPnPService{
		"ContentDirectory": &contentDirectoryService{
			server: s,
		},
		"ConnectionManager": &connectionManagerService{
			server: s,
		},
		"X_MS_MediaReceiverRegistrar": &mediaReceiverRegistrarService{
			server: s,
		},
	}

	// Setup the various http routes.
	r := http.NewServeMux()
	r.Handle(resPath, http.StripPrefix(resPath,
		http.HandlerFunc(s.resourceHandler)))
	if opt.LogTrace {
		r.Handle(rootDescPath, traceLogging(http.HandlerFunc(s.rootDescHandler)))
		r.Handle(serviceControlURL, traceLogging(http.HandlerFunc(s.serviceControlHandler)))
	} else {
		r.HandleFunc(rootDescPath, s.rootDescHandler)
		r.HandleFunc(serviceControlURL, s.serviceControlHandler)
	}
	r.Handle("/static/", http.StripPrefix("/static/",
		withHeader("Cache-Control", "public, max-age=86400",
			http.FileServer(data.Assets))))
	s.handler = logging(withHeader("Server", serverField, r))

	return s, nil
}

// UPnPService is the interface for the SOAP service.
type UPnPService interface {
	Handle(action string, argsXML []byte, r *http.Request) (respArgs map[string]string, err error)
	Subscribe(callback []*url.URL, timeoutSeconds int) (sid string, actualTimeout int, err error)
	Unsubscribe(sid string) error
}

// Formats the server as a string (used for logging.)
func (s *server) String() string {
	return fmt.Sprintf("DLNA server on %v", s.httpListenAddr)
}

// Returns version number as the model number.
func (s *server) ModelNumber() string {
	return conf.VERSION
}

// Renders the root device descriptor.
func (s *server) rootDescHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := data.GetTemplate()
	if err != nil {
		serveError(s, w, "Failed to load root descriptor template", err)
		return
	}

	buffer := new(bytes.Buffer)
	err = tmpl.Execute(buffer, s)
	if err != nil {
		serveError(s, w, "Failed to render root descriptor XML", err)
		return
	}

	w.Header().Set("content-type", `text/xml; charset="utf-8"`)
	w.Header().Set("cache-control", "private, max-age=60")
	w.Header().Set("content-length", strconv.FormatInt(int64(buffer.Len()), 10))
	_, err = buffer.WriteTo(w)
	if err != nil {
		// Network error
		log.Debugf("Error writing rootDesc: %v", err)
	}
}

// Handle a service control HTTP request.
func (s *server) serviceControlHandler(w http.ResponseWriter, r *http.Request) {
	soapActionString := r.Header.Get("SOAPACTION")
	soapAction, err := upnp.ParseActionHTTPHeader(soapActionString)
	if err != nil {
		serveError(s, w, "Could not parse SOAPACTION header", err)
		return
	}
	var env soap.Envelope
	if err := xml.NewDecoder(r.Body).Decode(&env); err != nil {
		serveError(s, w, "Could not parse SOAP request body", err)
		return
	}

	w.Header().Set("Content-Type", `text/xml; charset="utf-8"`)
	w.Header().Set("Ext", "")
	soapRespXML, code := func() ([]byte, int) {
		respArgs, err := s.soapActionResponse(soapAction, env.Body.Action, r)
		if err != nil {
			log.Errorf("Error invoking %v: %v", soapAction, err)
			upnpErr := upnp.ConvertError(err)
			return mustMarshalXML(soap.NewFault("UPnPError", upnpErr)), http.StatusInternalServerError
		}
		return marshalSOAPResponse(soapAction, respArgs), http.StatusOK
	}()
	bodyStr := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8" standalone="yes"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>%s</s:Body></s:Envelope>`, soapRespXML)
	w.WriteHeader(code)
	if _, err := w.Write([]byte(bodyStr)); err != nil {
		log.Infof("Error writing response: %v", err)
	}
}

// Handle a SOAP request and return the response arguments or UPnP error.
func (s *server) soapActionResponse(sa upnp.SoapAction, actionRequestXML []byte, r *http.Request) (map[string]string, error) {
	service, ok := s.services[sa.Type]
	if !ok {
		// TODO: What's the invalid service error?
		return nil, upnp.Errorf(upnp.InvalidActionErrorCode, "Invalid service: %s", sa.Type)
	}
	return service.Handle(sa.Action, actionRequestXML, r)
}

// Serves actual resources (media files).
func (s *server) resourceHandler(w http.ResponseWriter, r *http.Request) {
	realPath := s.RootDir + "/" + r.URL.Path
	ctx := r.Context()
	node, err := fs.Get(ctx, realPath, &fs.GetArgs{})
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if node.IsDir() {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(node.GetSize(), 10))

	// add some DLNA specific headers
	if r.Header.Get("getContentFeatures.dlna.org") != "" {
		w.Header().Set("contentFeatures.dlna.org", dms_dlna.ContentFeatures{
			SupportRange: true,
		}.String())
	}
	w.Header().Set("transferMode.dlna.org", "Streaming")

	link, _, err := fs.Link(ctx, realPath, model.LinkArgs{Header: r.Header, HttpReq: r})
	if err != nil {
		serveError(node, w, "Could not open resource", err)
		return
	}
	err = common.Proxy(w, r, link, node)
	if err != nil {
		serveError(node, w, "dlna proxy error", err)
		return
	}
}

// Serve runs the server - returns the error only if
// the listener was not started; does not block, so
// use s.Wait() to block on the listener indefinitely.
func (s *server) Serve() (err error) {
	if s.HTTPConn == nil {
		// Currently, the SSDP server only listens on an IPv4 multicast address.
		// Differentiate between two INADDR_ANY addresses,
		// so that 0.0.0.0 can only listen on IPv4 addresses.
		network := "tcp4"
		if strings.Count(s.httpListenAddr, ":") > 1 {
			network = "tcp"
		}
		s.HTTPConn, err = net.Listen(network, s.httpListenAddr)
		if err != nil {
			return
		}
	}

	go func() {
		s.startSSDP()
	}()

	go func() {
		utils.Log.Infof("Serving DLNA server on %s", s.HTTPConn.Addr().String())

		err := s.serveHTTP()
		if err != nil {
			utils.Log.Infof("Error on serving DLNA server: %v", err)
		}
	}()

	return nil
}

// Wait blocks while the listener is open.
func (s *server) Wait() {
	<-s.waitChan
}

func (s *server) Close() {
	err := s.HTTPConn.Close()
	if err != nil {
		log.Errorf("Error closing HTTP server: %v", err)
		return
	}
	close(s.waitChan)
}

// Run SSDP (multicast for server discovery) on all interfaces.
func (s *server) startSSDP() {
	active := 0
	stopped := make(chan struct{})
	for _, intf := range s.Interfaces {
		active++
		go func(intf2 net.Interface) {
			defer func() {
				stopped <- struct{}{}
			}()
			s.ssdpInterface(intf2)
		}(intf)
	}
	for active > 0 {
		<-stopped
		active--
	}
}

// Run SSDP server on an interface.
func (s *server) ssdpInterface(intf net.Interface) {
	// Figure out whether should an ip be announced
	ipfilterFn := func(ip net.IP) bool {
		listenaddr := s.HTTPConn.Addr().String()
		listenip := listenaddr[:strings.LastIndex(listenaddr, ":")]
		switch listenip {
		case "0.0.0.0":
			if strings.Contains(ip.String(), ":") {
				// Any IPv6 address should not be announced
				// because SSDP only listen on IPv4 multicast address
				return false
			}
			return true
		case "[::]":
			// In the @Serve() section, the default settings have been made to not listen on IPv6 addresses.
			// If actually still listening on [::], then allow to announce any address.
			return true
		default:
			if listenip == ip.String() {
				return true
			}
			return false
		}
	}

	// Figure out which HTTP location to advertise based on the interface IP.
	advertiseLocationFn := func(ip net.IP) string {
		url := url.URL{
			Scheme: "http",
			Host: (&net.TCPAddr{
				IP:   ip,
				Port: s.HTTPConn.Addr().(*net.TCPAddr).Port,
			}).String(),
			Path: rootDescPath,
		}
		return url.String()
	}

	_, err := intf.Addrs()
	if err != nil {
		panic(err)
	}

	// Note that the devices and services advertised here via SSDP should be
	// in agreement with the rootDesc XML descriptor that is defined above.
	ssdpServer := ssdp.Server{
		Interface: intf,
		Devices: []string{
			"urn:schemas-upnp-org:device:MediaServer:1"},
		Services: []string{
			"urn:schemas-upnp-org:service:ContentDirectory:1",
			"urn:schemas-upnp-org:service:ConnectionManager:1",
			"urn:microsoft.com:service:X_MS_MediaReceiverRegistrar:1"},
		IPFilter:       ipfilterFn,
		Location:       advertiseLocationFn,
		Server:         serverField,
		UUID:           s.RootDeviceUUID,
		NotifyInterval: s.AnnounceInterval,
		Logger:         alog.Default,
	}

	// An interface with these flags should be valid for SSDP.
	const ssdpInterfaceFlags = net.FlagUp | net.FlagMulticast

	if err := ssdpServer.Init(); err != nil {
		if intf.Flags&ssdpInterfaceFlags != ssdpInterfaceFlags {
			// Didn't expect it to work anyway.
			return
		}
		if strings.Contains(err.Error(), "listen") {
			// OSX has a lot of dud interfaces. Failure to create a socket on
			// the interface are what we're expecting if the interface is no
			// good.
			return
		}
		log.Errorf("Error creating ssdp server on %s: %s", intf.Name, err)
		return
	}
	defer ssdpServer.Close()
	utils.Log.Infof("Started SSDP on %v", intf.Name)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := ssdpServer.Serve(); err != nil {
			log.Errorf("%q: %q\n", intf.Name, err)
		}
	}()
	select {
	case <-s.waitChan:
		// Returning will close the server.
	case <-stopped:
	}
}

func (s *server) serveHTTP() error {
	srv := &http.Server{
		Handler: s.handler,
	}
	err := srv.Serve(s.HTTPConn)
	select {
	case <-s.waitChan:
		return nil
	default:
		return err
	}
}
