package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/conamu/mycorrizal/internal/nodosum"
	"github.com/conamu/mycorrizal/internal/packet"
)

func main() {
	fmt.Println("Pulse CLI v0.0.0")

	ctx, cancel := context.WithCancel(context.Background())

	caCert, err := os.ReadFile("ca.pem")
	if err != nil {
		log.Fatal(err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	cert, err := tls.LoadX509KeyPair("cert.pem", "key.pem")
	if err != nil {
		log.Fatal(err)
	}

	conn, err := net.Dial("tcp", "localhost:6969")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:   "localhost",
		RootCAs:      caCertPool,
		Certificates: []tls.Certificate{cert},
	})

	err = tlsConn.Handshake()
	if err != nil {
		log.Fatal(err)
	}

	conn = tlsConn

	buff := make([]byte, 4096)

	_, err = conn.Read(buff)
	if err != nil {
		log.Fatal(err)
	}

	pack, err := packet.Unpack(buff)
	if err != nil {
		log.Fatal(err)
	}

	if pack.Command != nodosum.HELLO {
		log.Fatal("Server handshake failed")
	}

	p, err := packet.Pack(nodosum.HELLO, []byte("CLI-"+string(pack.Data)), "")
	if err != nil {
		log.Fatal(err)
	}

	_, err = conn.Write(p)
	if err != nil {
		log.Fatal(err)
	}

	scanner := bufio.NewScanner(os.Stdin)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			for scanner.Scan() {
				s := scanner.Text()
				token := ""
				args := strings.Split(s, " ")
				if len(args) == 2 {
					token = args[1]
				}
				if len(args) == 0 {
					continue
				}

				if args[0] == "exit" {
					p, err := packet.Pack(nodosum.EXIT, nil, token)
					if err != nil {
						log.Fatal(err)
					}
					_, err = conn.Write(p)
					if err != nil {
						log.Fatal(err)
					}

					err = conn.Close()
					if err != nil {
						log.Fatal(err)
					}
					cancel()
					break
				}

				if args[0] == "id" {
					p, err := packet.Pack(nodosum.ID, nil, token)
					if err != nil {
						log.Fatal(err)
					}
					_, err = conn.Write(p)
					if err != nil {
						log.Fatal(err)
					}

					buff := make([]byte, 40960000)
					n, err := conn.Read(buff)
					if err != nil {
						log.Fatal(err)
					}
					pack, err := packet.Unpack(buff[:n])
					if err != nil {
						log.Fatal(err)
					}
					fmt.Println(string(pack.Data))
				}

				if args[0] == "set" {
					p, err := packet.Pack(nodosum.SET, []byte(args[2]), token)
					if err != nil {
						log.Fatal(err)
					}
					_, err = conn.Write(p)
					if err != nil {
						log.Fatal(err)
					}

					buff := make([]byte, 40960000)
					n, err := conn.Read(buff)
					if err != nil {
						log.Fatal(err)
					}
					pack, err := packet.Unpack(buff[:n])
					if err != nil {
						log.Fatal(err)
					}
					fmt.Println(string(pack.Data))
				}

				if args[0] == "GET" {
					p, err := packet.Pack(nodosum.GET, []byte(args[2]), token)
					if err != nil {
						log.Fatal(err)
					}
					_, err = conn.Write(p)
					if err != nil {
						log.Fatal(err)
					}

					buff := make([]byte, 40960000)
					n, err := conn.Read(buff)
					if err != nil {
						log.Fatal(err)
					}
					pack, err := packet.Unpack(buff[:n])
					if err != nil {
						log.Fatal(err)
					}
					fmt.Println(string(pack.Data))
				}
			}
		}
	}

}
