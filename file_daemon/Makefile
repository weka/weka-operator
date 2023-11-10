.PHONY: image
image: .make/image

.make:
	mkdir .make

SOURCES := Dockerfile weka_testing.service entrypoint.sh

.make/image: .make $(SOURCES)
	docker buildx bake
	touch .make/image
