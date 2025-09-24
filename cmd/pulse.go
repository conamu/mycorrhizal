package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/conamu/mycorrizal/internal/packet"
)

func main() {
	fmt.Println("Pulse CLI v0.0.0")

	ctx, cancel := context.WithCancel(context.Background())

	conn, err := net.Dial("tcp", "localhost:6969")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	buff := make([]byte, 40960000)

	_, err = conn.Read(buff)
	if err != nil {
		log.Fatal(err)
	}

	command, data, err := packet.Unpack(buff)
	if err != nil {
		log.Fatal(err)
	}

	if command != "HELLO" {
		log.Fatal("Server handshake failed")
	}

	p, err := packet.Pack("HELLO", []byte("CLI-"+string(data)))
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
				fmt.Println(s)
				if s == "id" {
					p, err := packet.Pack("ID", nil)
					if err != nil {
						log.Fatal(err)
					}
					_, err = conn.Write(p)
					if err != nil {
						log.Fatal(err)
					}
					idBuff := make([]byte, 40960000)
					n, err := conn.Read(idBuff)
					if err != nil {
						log.Fatal(err)
					}
					command, data, err := packet.Unpack(idBuff[:n])
					if err != nil {
						log.Fatal(err)
					}
					fmt.Println(command)
					fmt.Println(string(data))
				}
				if s == "exit" {
					err = conn.Close()
					if err != nil {
						log.Fatal(err)
					}
					cancel()
					break
				}
			}
		}
	}

}
