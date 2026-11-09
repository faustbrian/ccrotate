bin_dir := env("HOME") / ".local/bin"

# List the available recipes
default:
    @just --list

# Build ./ccrotate
build:
    go build -o ccrotate .

# Run from source, e.g. just run add work
run *args:
    go run . {{args}}

# Install ccrotate into bin_dir
install:
    mkdir -p "{{bin_dir}}"
    go build -o "{{bin_dir}}/ccrotate" .

# Remove the installed binary
uninstall:
    rm -f "{{bin_dir}}/ccrotate"

# Format the Go source
fmt:
    gofmt -w .

# Run the tests
test:
    go test ./...

# Run the linter, on the Go line it was built for
lint:
    GOTOOLCHAIN=go1.26.10 golangci-lint run

# Fail on unformatted code or vet findings
check:
    test -z "$(gofmt -l .)"
    go vet ./...

# Remove the local build
clean:
    rm -f ccrotate
