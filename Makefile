GO=	env CGO_CFLAGS=-D__EXTENSIONS__=1 GOCACHE=`pwd`/cache go

all: mailsink

mailsink: mailsink.go fts5query.go
	$(GO) build -v --tags fts5,nodeploy -o $@ mailsink.go fts5query.go

test:
	$(GO) test

solaris: clean
	env GOOS=illumos GOARCH=amd64 gmake mailsink

clean:
	-rm -rf bin src pkg mailsink *~ core search.db cache
