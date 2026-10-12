package main

// A minimal RESP (Redis wire protocol) client. The service only needs
// HGET/HGETALL/HSET/PUBLISH/SUBSCRIBE, so a hand-written client is smaller
// than the dependency it replaces and keeps the cross-compiled binary free
// of vendored code.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

type redisConn struct {
	conn net.Conn
	rd   *bufio.Reader
}

func redisConnect(addr string) (*redisConn, error) {
	c, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, err
	}
	return &redisConn{conn: c, rd: bufio.NewReader(c)}, nil
}

func (r *redisConn) close() { r.conn.Close() }

// cmdTimeout bounds one request/response pair. Without it, a server that
// stops answering while the TCP connection stays open parks the caller in a
// blocking read forever.
var cmdTimeout = 3 * time.Second

func (r *redisConn) setTimeout(d time.Duration) {
	if d <= 0 {
		r.conn.SetDeadline(time.Time{})
		return
	}
	r.conn.SetDeadline(time.Now().Add(d))
}

func (r *redisConn) send(args ...string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&sb, "$%d\r\n%s\r\n", len(a), a)
	}
	_, err := r.conn.Write([]byte(sb.String()))
	return err
}

func (r *redisConn) readLine() (string, error) {
	line, err := r.rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (r *redisConn) readReply() (interface{}, error) {
	line, err := r.readLine()
	if err != nil {
		return nil, err
	}
	if len(line) == 0 {
		return nil, fmt.Errorf("empty redis reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, fmt.Errorf("redis error: %s", line[1:])
	case ':':
		n, perr := strconv.ParseInt(line[1:], 10, 64)
		if perr != nil {
			return nil, fmt.Errorf("bad integer reply %q: %w", line, perr)
		}
		return n, nil
	case '$':
		size, perr := strconv.Atoi(line[1:])
		if perr != nil {
			return nil, fmt.Errorf("bad bulk length %q: %w", line, perr)
		}
		if size < 0 {
			return nil, nil
		}
		data := make([]byte, size+2)
		if _, err := io.ReadFull(r.rd, data); err != nil {
			return nil, err
		}
		return string(data[:size]), nil
	case '*':
		count, perr := strconv.Atoi(line[1:])
		if perr != nil {
			return nil, fmt.Errorf("bad array length %q: %w", line, perr)
		}
		if count < 0 {
			return nil, nil
		}
		arr := make([]interface{}, count)
		for i := range arr {
			arr[i], err = r.readReply()
			if err != nil {
				return nil, err
			}
		}
		return arr, nil
	}
	return nil, fmt.Errorf("unknown redis type: %c", line[0])
}

func (r *redisConn) hmget(hash string, fields ...string) ([]string, error) {
	r.setTimeout(cmdTimeout)
	defer r.setTimeout(0)

	args := make([]string, 2+len(fields))
	args[0] = "HMGET"
	args[1] = hash
	copy(args[2:], fields)
	if err := r.send(args...); err != nil {
		return nil, err
	}
	v, err := r.readReply()
	if err != nil {
		return nil, err
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("expected array from HMGET")
	}
	result := make([]string, len(arr))
	for i, item := range arr {
		if s, ok := item.(string); ok {
			result[i] = s
		}
	}
	return result, nil
}

func (r *redisConn) hgetall(hash string) (map[string]string, error) {
	r.setTimeout(cmdTimeout)
	defer r.setTimeout(0)

	if err := r.send("HGETALL", hash); err != nil {
		return nil, err
	}
	v, err := r.readReply()
	if err != nil {
		return nil, err
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil, fmt.Errorf("expected array from HGETALL")
	}
	m := make(map[string]string, len(arr)/2)
	for i := 0; i+1 < len(arr); i += 2 {
		k, _ := arr[i].(string)
		val, _ := arr[i+1].(string)
		m[k] = val
	}
	return m, nil
}

// hset sets one field and publishes the field name (not the value) to a
// notification channel so a watcher knows to re-read it.
func (r *redisConn) hset(hash, field, value, notifyChannel string) error {
	r.setTimeout(cmdTimeout)
	defer r.setTimeout(0)

	if err := r.send("HSET", hash, field, value); err != nil {
		return err
	}
	if _, err := r.readReply(); err != nil {
		return err
	}
	if notifyChannel == "" {
		return nil
	}
	if err := r.send("PUBLISH", notifyChannel, field); err != nil {
		return err
	}
	_, err := r.readReply()
	return err
}

func (r *redisConn) publish(channel, message string) (subscribers int64, err error) {
	r.setTimeout(cmdTimeout)
	defer r.setTimeout(0)

	if err := r.send("PUBLISH", channel, message); err != nil {
		return 0, err
	}
	reply, err := r.readReply()
	if err != nil {
		return 0, err
	}
	n, _ := reply.(int64)
	return n, nil
}
