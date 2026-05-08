BINARY = wlchat

.PHONY: build run clean

build:
	go build -o $(BINARY) ./cmd/wlchat

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)
