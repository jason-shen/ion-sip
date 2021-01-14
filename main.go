package main

import (
	"fmt"
	"github.com/pion/sdp/v2"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cloudwebrtc/go-sip-ua/pkg/account"
	"github.com/cloudwebrtc/go-sip-ua/pkg/media/rtp"
	"github.com/cloudwebrtc/go-sip-ua/pkg/session"
	"github.com/cloudwebrtc/go-sip-ua/pkg/stack"
	"github.com/cloudwebrtc/go-sip-ua/pkg/ua"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/ghettovoice/gosip/sip/parser"
	"github.com/ghettovoice/gosip/transport"
)

var (
	logger 	log.Logger
	udp		*rtp.RtpUDPStream
)

func createUdp() *rtp.RtpUDPStream {

	udp = rtp.NewRtpUDPStream("127.0.0.1", rtp.DefaultPortMin, rtp.DefaultPortMax, func(data []byte, raddr net.Addr) {
		logger.Infof("Rtp recevied: %v, laddr %s : raddr %s", len(data), udp.LocalAddr().String(), raddr)
		dest, _ := net.ResolveUDPAddr(raddr.Network(), raddr.String())
		logger.Infof("Echo rtp to %v", raddr)
		udp.Send(data, dest)
	}, logger)

	go udp.Read()

	return udp
}

func init() {
	logger = log.NewDefaultLogrusLogger().WithPrefix("Client")
}

func main() {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGABRT, syscall.SIGINT)
	stack := stack.NewSipStack(&stack.SipStackConfig{Extensions: []string{"replaces", "outbound"}, Dns: "8.8.8.8"}, logger)

	listen := "0.0.0.0:5080"
	logger.Infof("Listen => %s", listen)

	if err := stack.Listen("udp", listen); err != nil {
		logger.Panic(err)
	}

	if err := stack.Listen("tcp", listen); err != nil {
		logger.Panic(err)
	}

	tlsOptions := &transport.TLSConfig{Cert: "certs/cert.pem", Key: "certs/key.pem"}

	if err := stack.ListenTLS("wss", "0.0.0.0:5091", tlsOptions); err != nil {
		logger.Panic(err)
	}
// start
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		panic(err)
	}

	pc.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		logger.Infof("Connection State has changed %s \n", connectionState.String())
	})
	oggFile, err := oggwriter.New("output.ogg", 48000, 2)
	if err != nil {
		panic(err)
	}
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "audio")
	if err != nil {
		logger.Errorf("ERROR sendTrackVideoToCaller NewTrackLocalStaticRTP audio: %v\n", err)
	}
	_, err = pc.AddTrack(audioTrack)
	if err != nil {
		logger.Errorf("ERROR sendTrackVideoToCaller AddTrack audio : %v\n", err)
	}
	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		fmt.Println("Got Opus track, saving to disk as output.ogg")
		for {
			rtpPacket, _, readErr := track.ReadRTP()
			if readErr != nil {
				panic(readErr)
			}
			if readErr := oggFile.WriteRTP(rtpPacket); readErr != nil {
				panic(readErr)
			}
		}
	})

	if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		panic(err)
	}
	if err := pc.SetLocalDescription(offer); err != nil {
		panic(err)
	}

	//end
	ua := ua.NewUserAgent(&ua.UserAgentConfig{
		UserAgent: "Go Sip Client/1.0.0",
		SipStack:  stack,
	}, logger)

	ua.InviteStateHandler = func(sess *session.Session, req *sip.Request, resp *sip.Response, state session.Status) {
		logger.Infof("InviteStateHandler: state => %v, type => %s", state, sess.Direction())
		switch state {
		case session.InviteReceived:
			logger.Infof("invited!!!")
			//udp = createUdp()
			//udpLaddr := udp.LocalAddr()
			//sdpold := mock.BuildLocalSdp(udpLaddr.IP.String(), udpLaddr.Port)
			//logger.Infof("old sdp", sdpold)
			//logger.Infof("remote sdp", sess.RemoteSdp())
			sdp := rewriteSDP(sess.RemoteSdp())
			sdp += "a=mid:0\r\n"
			logger.Infof("sdp=>>>", sdp)
			sess.ProvideAnswer(sdp)
			sess.Accept(200)
			if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp}); err != nil {
				panic(err)
			}
			fmt.Println("answering", sess.Request().Body())
			break
		case session.InviteSent:
			logger.Infof("answered", resp)
			break
		case session.Canceled:
			fallthrough
		case session.Failure:
			logger.Errorf("failed!!", sess.Response().Body())
			break
		case session.Terminated:
			udp.Close()
		}
	}

	ua.RegisterStateHandler = func(state account.RegisterState) {
		logger.Infof("RegisterStateHandler: user => %s, state => %v, expires => %v", state.Account.AuthInfo.AuthUser, state.StatusCode, state.Expiration)
	}

	uri, err := parser.ParseUri("sip:100@10.157.226.130")
	if err != nil {
		logger.Error(err)
	}

	profile := account.NewProfile(uri.Clone(), "goSIP",
		&account.AuthInfo{
			AuthUser: "100",
			Password: "100",
			Realm:    "",
		},
		1800,
	)

	recipient, err := parser.ParseSipUri("sip:200@10.157.226.130;transport=udp")
	if err != nil {
		logger.Error(err)
	}

	 go ua.SendRegister(profile, recipient, profile.Expires)
	time.Sleep(time.Second * 3)

	 udp = createUdp()
	// udpLaddr := udp.LocalAddr()
	//sdp := mock.BuildLocalSdp(udpLaddr.IP.String(), udpLaddr.Port)
	////
	sdp := offer.SDP
	//logger.Infof("offer SDP => ", sdp)
	called, err2 := parser.ParseUri("sip:200@10.157.226.130")
	if err2 != nil {
		logger.Error(err)
	}

	go ua.Invite(profile, called, recipient, &sdp)

	//time.Sleep(time.Second * 3)
	//go ua.SendRegister(profile, recipient, 0)

	<-stop

	ua.Shutdown()
}

func rewriteSDP(in string) string {
	parsed := &sdp.SessionDescription{}
	if err := parsed.Unmarshal([]byte(in)); err != nil {
		panic(err)
	}

	// Reverse global attributes
	for i, j := 0, len(parsed.Attributes)-1; i < j; i, j = i+1, j-1 {
		parsed.Attributes[i], parsed.Attributes[j] = parsed.Attributes[j], parsed.Attributes[i]
	}

	parsed.MediaDescriptions[0].Attributes = append(parsed.MediaDescriptions[0].Attributes, sdp.Attribute{
		Key:   "candidate",
		Value: "79019993 1 udp 1686052607 1.1.1.1 9 typ srflx",
	})

	out, err := parsed.Marshal()
	if err != nil {
		panic(err)
	}

	return string(out)
}

func CompleteTheOfferSDP(sdp string) string {
	//ADD : a=sendrecv and a=mid if missing and a=ice-lite
	sdpSplited := strings.Split(sdp, "\r\n")
	for i := 0; i < len(sdpSplited); i++ {
		if strings.HasPrefix(sdpSplited[i], "t=0 0") { //ADD ice-lite
			sdpSplited[i] += "\r\na=ice-lite"
		} else if strings.HasPrefix(sdpSplited[i], "m=audio") {
			sdpSplited[i] = "a=sendrecv\r\n" + sdpSplited[i]
			sdpSplited[i] = "a=mid:0\r\n" + sdpSplited[i]
		}
	}
	return strings.Join(sdpSplited, "\r\n") + "\r\n"
}