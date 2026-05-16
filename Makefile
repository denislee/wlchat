BINARY = wlchat
LDFLAGS = -s -w

.PHONY: build run clean

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/wlchat

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)
