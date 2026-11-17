package bklite_rocketmq

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"
)

const (
	codeGetBrokerRuntimeInfo    = 28
	codeGetBrokerClusterInfo    = 106
	codeGetAllSubscriptionGroup = 201
	codeGetTopicStatsInfo       = 202
	codeGetAllTopicList         = 206
	codeGetConsumeStats         = 208
	remotingSuccess             = 0
)

var rpcOpaque int32

type remotingHeader struct {
	Code      int               `json:"code"`
	Language  string            `json:"language"`
	Version   int               `json:"version"`
	Opaque    int32             `json:"opaque"`
	Flag      int               `json:"flag"`
	Remark    string            `json:"remark,omitempty"`
	ExtFields map[string]string `json:"extFields"`
}

func invoke(ctx context.Context, addr string, code int, ext map[string]string) ([]byte, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(10 * time.Second)
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return nil, err
	}

	header := remotingHeader{
		Code:      code,
		Language:  "GO",
		Version:   317,
		Opaque:    atomic.AddInt32(&rpcOpaque, 1),
		ExtFields: ext,
	}
	if header.ExtFields == nil {
		header.ExtFields = map[string]string{}
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	frameLen := 4 + len(headerBytes)
	buf := make([]byte, 4+frameLen)
	binary.BigEndian.PutUint32(buf[0:4], uint32(frameLen))
	buf[4] = 0
	buf[5] = byte((len(headerBytes) >> 16) & 0xFF)
	buf[6] = byte((len(headerBytes) >> 8) & 0xFF)
	buf[7] = byte(len(headerBytes) & 0xFF)
	copy(buf[8:], headerBytes)
	if _, err = conn.Write(buf); err != nil {
		return nil, err
	}

	var total uint32
	if err = binary.Read(conn, binary.BigEndian, &total); err != nil {
		return nil, err
	}
	if total < 4 || total > 32*1024*1024 {
		return nil, fmt.Errorf("rocketmq frame length %d", total)
	}
	payload := make([]byte, total)
	if _, err = io.ReadFull(conn, payload); err != nil {
		return nil, err
	}
	headerLen := binary.BigEndian.Uint32(payload[:4]) & 0xFFFFFF
	if int(4+headerLen) > len(payload) {
		return nil, fmt.Errorf("rocketmq header length %d", headerLen)
	}
	var resp remotingHeader
	if err = json.Unmarshal(payload[4:4+headerLen], &resp); err != nil {
		return nil, err
	}
	if resp.Code != remotingSuccess {
		if resp.Remark == "" {
			return nil, fmt.Errorf("rocketmq code %d", resp.Code)
		}
		return nil, fmt.Errorf("rocketmq code %d: %s", resp.Code, resp.Remark)
	}
	return relaxRocketMQJSON(payload[4+headerLen:]), nil
}
