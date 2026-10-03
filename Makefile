# Pulse — common tasks. `make demo` builds everything and starts the server.

.PHONY: all web build run demo sim test check tunnel recordings clean

all: build

web/node_modules: web/package.json
	cd web && npm install
	@touch web/node_modules

web: web/node_modules
	cd web && npm run build

build: web
	go build -o bin/pulse ./server/cmd/pulse
	go build -o bin/sim ./server/cmd/sim
	go build -o bin/dashtail ./server/cmd/dashtail

run:
	go run ./server/cmd/pulse $(ARGS)

demo: build
	./bin/pulse $(ARGS)

# make sim SCENARIO=dance N=8
SCENARIO ?= wave
N ?= 8
sim:
	go run ./server/cmd/sim -n $(N) -scenario $(SCENARIO) $(ARGS)

test:
	go vet ./...
	go test ./...

check: test
	cd web && npm run typecheck

# Phones need HTTPS for motion sensors. Quick tunnel (random URL):
tunnel:
	cloudflared tunnel --url http://localhost:8080

# Regenerate the simulated safety-net recordings.
recordings:
	for s in wave dance shove; do go run ./server/cmd/sim -n 8 -scenario $$s -seed 42 -duration 70s -out recordings/sim-$$s.jsonl; done

clean:
	rm -rf bin web/phone/dist web/dashboard/dist
