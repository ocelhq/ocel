package clitest

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

func ServePostgres(t *testing.T, serverVersionNum string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for postgres: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go answerPostgres(conn, serverVersionNum)
		}
	}()
	return ln.Addr().String()
}

func answerPostgres(conn net.Conn, serverVersionNum string) {
	defer conn.Close()
	in := bufio.NewReader(conn)
	if _, err := readPostgresBody(in); err != nil {
		return
	}
	reply := postgresMessage('R', binary.BigEndian.AppendUint32(nil, 0))
	reply = append(reply, postgresMessage('Z', []byte{'I'})...)
	if _, err := conn.Write(reply); err != nil {
		return
	}
	for {
		kind, err := in.ReadByte()
		if err != nil || kind == 'X' {
			return
		}
		if _, err := readPostgresBody(in); err != nil {
			return
		}
		if kind != 'Q' {
			continue
		}
		if _, err := conn.Write(postgresVersionRow(serverVersionNum)); err != nil {
			return
		}
	}
}

func readPostgresBody(in *bufio.Reader) ([]byte, error) {
	var size uint32
	if err := binary.Read(in, binary.BigEndian, &size); err != nil {
		return nil, err
	}
	body := make([]byte, size-4)
	_, err := io.ReadFull(in, body)
	return body, err
}

func postgresMessage(kind byte, body []byte) []byte {
	out := []byte{kind}
	out = binary.BigEndian.AppendUint32(out, uint32(len(body)+4))
	return append(out, body...)
}

func postgresVersionRow(serverVersionNum string) []byte {
	var fields []byte
	fields = binary.BigEndian.AppendUint16(fields, 2)
	for _, column := range []struct {
		name string
		oid  uint32
		size int16
	}{{"?column?", 23, 4}, {"current_setting", 25, -1}} {
		fields = append(fields, column.name...)
		fields = append(fields, 0)
		fields = binary.BigEndian.AppendUint32(fields, 0)
		fields = binary.BigEndian.AppendUint16(fields, 0)
		fields = binary.BigEndian.AppendUint32(fields, column.oid)
		fields = binary.BigEndian.AppendUint16(fields, uint16(column.size))
		fields = binary.BigEndian.AppendUint32(fields, 0xFFFFFFFF)
		fields = binary.BigEndian.AppendUint16(fields, 0)
	}

	var row []byte
	row = binary.BigEndian.AppendUint16(row, 2)
	for _, value := range []string{"1", serverVersionNum} {
		row = binary.BigEndian.AppendUint32(row, uint32(len(value)))
		row = append(row, value...)
	}

	out := postgresMessage('T', fields)
	out = append(out, postgresMessage('D', row)...)
	out = append(out, postgresMessage('C', append([]byte("SELECT 1"), 0))...)
	return append(out, postgresMessage('Z', []byte{'I'})...)
}
