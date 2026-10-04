# Pulse — common tasks. `make demo` builds everything and starts the server.

.PHONY: all web build run demo sim test check env doctor tunnel recordings eval loadtest clean

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

# make sim SCENARIO=dance N=24 (crowd layout; ARGS="-layout line -n 8" for the line demo)
SCENARIO ?= wave
N ?= 24
sim:
	go run ./server/cmd/sim -n $(N) -scenario $(SCENARIO) $(ARGS)

# Detector over every scenario × SEEDS random crowds → docs/eval.json, docs/EVAL.md.
SEEDS ?= 20
eval:
	go run ./server/cmd/eval -seeds $(SEEDS)

# Fake phones against the running server → docs/loadtest.md. make loadtest N=1000 (or N=500,1000).
loadtest:
	go run ./server/cmd/loadtest -n $(N) -url ws://localhost:8080/ws/phone -duration 60s $(ARGS)

test:
	go vet ./...
	go test ./...

check: test
	cd web && npm run typecheck

# Ask for each secret and write .env, then test it.
env:
	./scripts/setup-env.sh

# Test the services in .env without starting the server.
doctor:
	go run ./server/cmd/pulse -check

# Phones need HTTPS for motion sensors. Quick tunnel (random URL):
tunnel:
	cloudflared tunnel --url http://localhost:8080

# Regenerate the simulated safety-net recordings.
recordings:
	for s in wave dance shove; do go run ./server/cmd/sim -layout line -n 8 -scenario $$s -seed 42 -duration 70s -out recordings/sim-$$s.jsonl; done

clean:
	rm -rf bin web/phone/dist web/dashboard/dist
