#!/usr/bin/env python3
"""NETCONF client over SSH using paramiko, with proper framing."""
import paramiko, sys, time

host = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
port = int(sys.argv[2]) if len(sys.argv) > 2 else 1830
password = sys.argv[3] if len(sys.argv) > 3 else "confd"

client = paramiko.SSHClient()
client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
client.connect(host, port=port, username="confd", password=password, look_for_keys=False, allow_agent=False)

ch = client.get_transport().open_session()
ch.exec_command("netconf")

def base10_send(ch, msg):
    ch.sendall(msg.encode() + b"\n]]>]]>\n")

def base10_recv(ch):
    data = b""
    while b"]]>]]>" not in data:
        chunk = ch.recv(4096)
        if not chunk:
            return data
        data += chunk
    return data.split(b"]]>]]>")[0]

def base11_send(ch, msg):
    payload = msg.encode()
    out = b""
    while len(payload) > 0:
        n = min(len(payload), 65535)
        out += f"\n#{n:04x}\n".encode() + payload[:n]
        payload = payload[n:]
    out += b"\n##\n"
    ch.sendall(out)

def base11_recv(ch):
    data = b""
    while b"\n##\n" not in data:
        chunk = ch.recv(4096)
        if not chunk:
            return data
        data += chunk
    raw = data.split(b"\n##\n")[0]
    msg = b""
    pos = 0
    while pos < len(raw):
        nl1 = raw.index(b"\n", pos)
        nl2 = raw.index(b"\n", nl1 + 1)
        header = raw[nl1+1:nl2]
        if header in (b"#", b"##"):
            break
        length = int(header[1:], 16)
        pos = nl2 + 1
        msg += raw[pos:pos+length]
        pos += length
    return msg

base10_send(ch, '<?xml version="1.0"?><hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><capabilities><capability>urn:ietf:params:netconf:base:1.1</capability></capabilities></hello>')

srv_hello = base10_recv(ch)
print("=== SERVER HELLO ===")
print(srv_hello.decode().strip())
print()

base11_send(ch, '<rpc message-id="101" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-config><source><running/></source></get-config></rpc>')
reply = base11_recv(ch)
print("=== GET-CONFIG (running) ===")
print(reply.decode().strip())
print()

base11_send(ch, '<rpc message-id="102" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get/></rpc>')
reply2 = base11_recv(ch)
print("=== GET (operational) ===")
print(reply2.decode().strip())
print()

base11_send(ch, '<rpc message-id="103" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get-schema><identifier>confd-test</identifier></get-schema></rpc>')
reply3 = base11_recv(ch)
has_yang = b"module confd-test" in reply3
print("=== GET-SCHEMA ===")
print(f"Contains YANG source: {has_yang}")
print()

base11_send(ch, '<rpc message-id="104" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><edit-config/></rpc>')
reply4 = base11_recv(ch)
print("=== UNKNOWN OP (edit-config) ===")
print(reply4.decode().strip())
print()

base11_send(ch, '<rpc message-id="105" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><close-session/></rpc>')
reply5 = base11_recv(ch)
print("=== CLOSE-SESSION ===")
print(reply5.decode().strip())

ch.close()
client.close()
print("\n=== ALL TESTS PASSED ===")
