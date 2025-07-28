GO=	env CGO_CFLAGS=-D__EXTENSIONS__=1 GOCACHE=`pwd`/cache go

all: mailsink

mailsink: mailsink.go fts5query.go
	$(GO) build -v --tags fts5,nodeploy -o $@ mailsink.go fts5query.go

mailsink-static: mailsink.go fts5query.go
	env CGO_ENABLED=1 CGO_CFLAGS="-D_LARGEFILE64_SOURCE" \
	$(GO) build -v --tags "fts5,nodeploy,osusergo,netgo,sqlite_omit_load_extension" \
	-ldflags '-linkmode external -extldflags "-static -lm -ldl -lpthread"' \
	-o mailsink mailsink.go fts5query.go

docker-build:
	docker build --tag mailsink:latest .

docker-stop:
	docker ps|awk '/mailsink/{print $$1}'|xargs -r -n 1 docker kill
	-docker kill mailsink
	-docker rm mailsink

docker: docker-stop docker-build
	docker run -d \
		--name mailsink \
		-v mailsink-data:/data \
		-p 2525:2525 \
		-p 8080:8080 \
		mailsink:latest

test:
	$(GO) test

clean:
	-rm -rf bin src pkg mailsink *~ core search.db cache
