// Package cli implements an interactive NETCONF client shell, similar to
// netopeer2-cli. It uses nemith.io/netconf's client Session to connect
// to a NETCONF server over SSH and provides a prompt for typing
// NETCONF operations.
package cli

import (
	"bufio"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"nemith.io/netconf"
	nssh "nemith.io/netconf/transport/ssh"

	gossh "golang.org/x/crypto/ssh"
)

// Shell is the interactive NETCONF client shell.
type Shell struct {
	session   *netconf.Session
	transport *nssh.Transport
	msgID     atomic.Uint64
	reader    *bufio.Reader
}

// New returns a new Shell reading from os.Stdin.
func New() *Shell {
	return &Shell{
		reader: bufio.NewReader(os.Stdin),
	}
}

// Run starts the interactive prompt loop. It blocks until the user
// types "quit" or "exit", or until EOF on stdin.
func (s *Shell) Run() error {
	fmt.Println("confd interactive NETCONF shell")
	fmt.Println("Type 'help' for available commands, 'quit' to exit.")
	for {
		prompt := "confd> "
		if s.session != nil {
			prompt = fmt.Sprintf("confd(%d)> ", s.session.SessionID())
		}
		fmt.Print(prompt)

		line, err := s.reader.ReadString('\n')
		if err == io.EOF {
			fmt.Println()
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		cmd, args := parseCommand(line)
		var cmdErr error
		switch cmd {
		case "quit", "exit":
			s.disconnect()
			return nil
		case "help":
			s.printHelp()
		case "connect":
			cmdErr = s.cmdConnect(args)
		case "disconnect":
			s.disconnect()
			fmt.Println("Disconnected.")
		case "show":
			cmdErr = s.cmdShow(args)
		case "raw":
			cmdErr = s.cmdRaw(args)
		case "get":
			cmdErr = s.cmdGet(args)
		case "get-config":
			cmdErr = s.cmdGetConfig(args)
		case "edit-config":
			cmdErr = s.cmdEditConfig(args)
		case "copy-config":
			cmdErr = s.cmdCopyConfig(args)
		case "delete-config":
			cmdErr = s.cmdDeleteConfig(args)
		case "lock":
			cmdErr = s.cmdLock(args)
		case "unlock":
			cmdErr = s.cmdUnlock(args)
		case "commit":
			cmdErr = s.cmdCommit(args)
		case "discard-changes":
			cmdErr = s.cmdDiscardChanges(args)
		case "validate":
			cmdErr = s.cmdValidate(args)
		case "get-schema":
			cmdErr = s.cmdGetSchema(args)
		case "kill-session":
			cmdErr = s.cmdKillSession(args)
		case "close-session":
			cmdErr = s.cmdCloseSession(args)
		default:
			fmt.Printf("Unknown command: %s (type 'help' for commands)\n", cmd)
		}
		if cmdErr != nil {
			fmt.Printf("Error: %v\n", cmdErr)
		}
	}
}

// parseCommand splits a line into command + args.
func parseCommand(line string) (cmd string, args []string) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

// flagValue finds --flag=value or --flag value in args.
func flagValue(args []string, flag string, def string) string {
	prefix := "--" + flag + "="
	for i, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
		if a == "--"+flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return def
}

// requireSession returns an error if not connected.
func (s *Shell) requireSession() error {
	if s.session == nil {
		return fmt.Errorf("not connected (use 'connect <host:port>')")
	}
	return nil
}

// nextMsgID returns the next message-id for RPCs.
func (s *Shell) nextMsgID() string {
	return fmt.Sprintf("%d", s.msgID.Add(1))
}

// printHelp lists available commands.
func (s *Shell) printHelp() {
	fmt.Println(`Available commands:
  connect <host:port> [--user <u>] [--password <p>]  Connect to a NETCONF server
  disconnect               Close the current session
  show session             Show session info (id, capabilities)
  show capabilities        List server capabilities
  get [--filter <xpath>]   <get> (operational datastore)
  get-config --source <ds> [--filter <xpath>]  <get-config>
  edit-config --target <ds> --config <xml>     <edit-config>
  copy-config --target <ds> --source <ds|config>  <copy-config>
  delete-config --target <startup>             <delete-config>
  lock --target <ds>                            <lock>
  unlock --target <ds>                          <unlock>
  commit                                        <commit>
  discard-changes                               <discard-changes>
  validate --source <ds>                        <validate>
  get-schema --identifier <module>             <get-schema>
  kill-session --session-id <N>                 <kill-session>
  close-session                                 <close-session>
  raw <xml>                  Send raw XML as an <rpc> and print the reply
  quit / exit                Exit the shell`)
}

// --- session commands ---

func (s *Shell) cmdConnect(args []string) error {
	if s.session != nil {
		s.disconnect()
	}
	addr := flagValue(args, "addr", "")
	if addr == "" && len(args) > 0 && !strings.HasPrefix(args[0], "--") {
		addr = args[0]
	}
	if addr == "" {
		addr = "127.0.0.1:830"
	}
	user := flagValue(args, "user", "confd")
	password := flagValue(args, "password", "")

	fmt.Printf("Connecting to %s...\n", addr)
	sshCfg := &gossh.ClientConfig{
		User:            user,
		Auth:            []gossh.AuthMethod{gossh.Password(password)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	}
	tr, err := nssh.Dial(context.Background(), "tcp", addr, sshCfg)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	sess, err := netconf.NewSession(tr)
	if err != nil {
		_ = tr.Close()
		return fmt.Errorf("hello: %w", err)
	}
	s.session = sess
	s.transport = tr
	fmt.Printf("Session %d established\n", sess.SessionID())
	return nil
}

func (s *Shell) disconnect() {
	if s.session != nil {
		_ = s.session.Close(context.Background())
		s.session = nil
	}
	if s.transport != nil {
		_ = s.transport.Close()
		s.transport = nil
	}
}

func (s *Shell) cmdShow(args []string) error {
	if s.session == nil {
		return fmt.Errorf("not connected")
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: show <session|capabilities>")
	}
	switch args[0] {
	case "session":
		fmt.Printf("Session ID: %d\n", s.session.SessionID())
		fmt.Printf("Client capabilities: %d\n", s.session.ClientCaps().Len())
		fmt.Printf("Server capabilities: %d\n", s.session.ServerCaps().Len())
	case "capabilities":
		fmt.Println("Server capabilities:")
		for cap := range s.session.ServerCaps().All() {
			fmt.Printf("  %s\n", cap)
		}
	default:
		return fmt.Errorf("unknown show: %s (use 'show session' or 'show capabilities')", args[0])
	}
	return nil
}

// --- raw RPC ---

func (s *Shell) cmdRaw(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	xmlStr := strings.Join(args, " ")
	if xmlStr == "" {
		return fmt.Errorf("usage: raw <xml>")
	}
	ctx := context.Background()
	rpc := &netconf.RPC{
		MessageID: s.nextMsgID(),
		Operation: netconf.RawXML(xmlStr),
	}
	msg, err := s.session.Do(ctx, rpc)
	if err != nil {
		return err
	}
	defer msg.Close()
	raw, err := io.ReadAll(msg)
	if err != nil {
		return err
	}
	printXML(raw)
	return nil
}

// --- NETCONF operations ---

func (s *Shell) cmdGet(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	filter := flagValue(args, "filter", "")
	ctx := context.Background()
	type getOp struct {
		XMLName xml.Name `xml:"get"`
		Filter  *filterType `xml:"filter,omitempty"`
	}
	op := &getOp{}
	if filter != "" {
		op.Filter = &filterType{Type: "xpath", Select: filter}
	}
	return s.execAndPrint(ctx, op)
}

type filterType struct {
	XMLName xml.Name `xml:"filter"`
	Type    string   `xml:"type,attr,omitempty"`
	Select  string   `xml:"select,attr,omitempty"`
}

// dsRef wraps a datastore name as inner XML so it serializes as
// <source><running/></source> instead of <source>running</source>.
type dsRef struct {
	Data []byte `xml:",innerxml"`
}

func newDSRef(name string) dsRef {
	return dsRef{Data: []byte("<" + name + "/>")}
}

func (s *Shell) cmdGetConfig(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	source := flagValue(args, "source", "running")
	filter := flagValue(args, "filter", "")
	ctx := context.Background()
	type getConfigOp struct {
		XMLName xml.Name    `xml:"get-config"`
		Source  dsRef       `xml:"source"`
		Filter  *filterType `xml:"filter,omitempty"`
	}
	op := &getConfigOp{Source: newDSRef(source)}
	if filter != "" {
		op.Filter = &filterType{Type: "xpath", Select: filter}
	}
	return s.execAndPrint(ctx, op)
}

func (s *Shell) cmdEditConfig(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	target := flagValue(args, "target", "running")
	defaultOp := flagValue(args, "default-operation", "merge")
	config := flagValue(args, "config", "")
	configFile := flagValue(args, "file", "")
	if config == "" && configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return fmt.Errorf("read file: %w", err)
		}
		config = string(data)
	}
	if config == "" {
		return fmt.Errorf("usage: edit-config --target <ds> --config <xml> | --file <path>")
	}
	ctx := context.Background()
	type editConfigOp struct {
		XMLName      xml.Name    `xml:"edit-config"`
		Target       dsRef       `xml:"target"`
		DefaultOp    string      `xml:"default-operation"`
		Config       netconf.RawXML `xml:"config"`
	}
	op := &editConfigOp{
		Target:    newDSRef(target),
		DefaultOp: defaultOp,
		Config:    netconf.RawXML(config),
	}
	return s.execAndPrint(ctx, op)
}


func (s *Shell) cmdCopyConfig(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	target := flagValue(args, "target", "startup")
	source := flagValue(args, "source", "running")
	ctx := context.Background()
	type copyConfigOp struct {
		XMLName xml.Name   `xml:"copy-config"`
		Target  dsRef `xml:"target"`
		Source  dsRef `xml:"source"`
	}
	op := &copyConfigOp{
		Target: newDSRef(target),
		Source: newDSRef(source),
	}
	return s.execAndPrint(ctx, op)
}

func (s *Shell) cmdDeleteConfig(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	target := flagValue(args, "target", "startup")
	ctx := context.Background()
	type deleteConfigOp struct {
		XMLName xml.Name   `xml:"delete-config"`
		Target  dsRef `xml:"target"`
	}
	op := &deleteConfigOp{Target: newDSRef(target)}
	return s.execAndPrint(ctx, op)
}

func (s *Shell) cmdLock(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	target := flagValue(args, "target", "running")
	ctx := context.Background()
	type lockOp struct {
		XMLName xml.Name   `xml:"lock"`
		Target  dsRef `xml:"target"`
	}
	op := &lockOp{Target: newDSRef(target)}
	return s.execAndPrint(ctx, op)
}

func (s *Shell) cmdUnlock(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	target := flagValue(args, "target", "running")
	ctx := context.Background()
	type unlockOp struct {
		XMLName xml.Name   `xml:"unlock"`
		Target  dsRef `xml:"target"`
	}
	op := &unlockOp{Target: newDSRef(target)}
	return s.execAndPrint(ctx, op)
}

func (s *Shell) cmdCommit(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	ctx := context.Background()
	type commitOp struct {
		XMLName xml.Name `xml:"commit"`
	}
	return s.execAndPrint(ctx, &commitOp{})
}

func (s *Shell) cmdDiscardChanges(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	ctx := context.Background()
	type discardOp struct {
		XMLName xml.Name `xml:"discard-changes"`
	}
	return s.execAndPrint(ctx, &discardOp{})
}

func (s *Shell) cmdValidate(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	source := flagValue(args, "source", "running")
	ctx := context.Background()
	type validateOp struct {
		XMLName xml.Name   `xml:"validate"`
		Source  dsRef `xml:"source"`
	}
	op := &validateOp{Source: newDSRef(source)}
	return s.execAndPrint(ctx, op)
}

func (s *Shell) cmdGetSchema(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	ident := flagValue(args, "identifier", "")
	if ident == "" {
		return fmt.Errorf("usage: get-schema --identifier <module>")
	}
	version := flagValue(args, "version", "")
	ctx := context.Background()
	type getSchemaOp struct {
		XMLName     xml.Name `xml:"get-schema"`
		Identifier  string   `xml:"identifier"`
		Version     string   `xml:"version,omitempty"`
		Format      string   `xml:"format,omitempty"`
	}
	op := &getSchemaOp{Identifier: ident, Version: version, Format: "yang"}
	return s.execAndPrint(ctx, op)
}

func (s *Shell) cmdKillSession(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	sid := flagValue(args, "session-id", "")
	if sid == "" {
		return fmt.Errorf("usage: kill-session --session-id <N>")
	}
	ctx := context.Background()
	type killSessionOp struct {
		XMLName   xml.Name `xml:"kill-session"`
		SessionID string  `xml:"session-id"`
	}
	op := &killSessionOp{SessionID: sid}
	return s.execAndPrint(ctx, op)
}

func (s *Shell) cmdCloseSession(args []string) error {
	if err := s.requireSession(); err != nil {
		return err
	}
	ctx := context.Background()
	type closeSessionOp struct {
		XMLName xml.Name `xml:"close-session"`
	}
	if err := s.execAndPrint(ctx, &closeSessionOp{}); err != nil {
		return err
	}
	s.disconnect()
	return nil
}

// --- helpers ---

// execAndPrint sends an RPC via Session.Do and prints the reply.
func (s *Shell) execAndPrint(ctx context.Context, op any) error {
	rpc := &netconf.RPC{
		MessageID: s.nextMsgID(),
		Operation: op,
	}
	msg, err := s.session.Do(ctx, rpc)
	if err != nil {
		return err
	}
	defer msg.Close()
	raw, err := io.ReadAll(msg)
	if err != nil {
		return err
	}
	printXML(raw)
	return nil
}

// printXML pretty-prints XML by re-encoding with indentation.
func printXML(data []byte) {
	var buf strings.Builder
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Println(string(data))
			return
		}
		enc.EncodeToken(tok)
	}
	enc.Flush()
	if buf.Len() > 0 {
		fmt.Println(buf.String())
		return
	}
	fmt.Println(string(data))
}
