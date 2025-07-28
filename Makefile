GO=	env CGO_CFLAGS=-D__EXTENSIONS__=1 GOCACHE=`pwd`/cache go

all: mailsink

mailsink: mailsink.go fts5query.go
	$(GO) build -v --tags fts5,nodeploy -o $@ mailsink.go fts5query.go

docker-build:
	docker build --tag mailsink:latest .

docker: docker-build
	docker run -d \
		-v mailsink-data:/data \
		-p 2525:2525 \
		-p 8080:8080 \
		mailsink:latest

test:
	$(GO) test

clean:
	-rm -rf bin src pkg mailsink *~ core search.db cache
