# Binário único, Go puro (sem CGO), para PC e Termux (Android arm64).
BIN      := bin/ideias
PKG      := ./cmd/ideias
GOFLAGS  := -trimpath
LDFLAGS  := -s -w

export CGO_ENABLED := 0

.PHONY: build build-android test vet clean

# Build para a máquina atual (no Termux, já sai android/arm64).
build:
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)$(if $(filter windows,$(shell go env GOOS)),.exe,) $(PKG)

# Cross-compile do PC para o celular.
build-android:
	GOOS=android GOARCH=arm64 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN)-android-arm64 $(PKG)

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin
