FROM ubuntu:latest

VOLUME ["/mnt/root"]
WORKDIR /app

# Copy the systemd unit into the image
# We'll move it onto a volume at runtime
COPY --chown=root:root weka_testing.service /app/weka_testing.service
COPY --chown=root:root entrypoint.sh /app/entrypoint.sh

ENTRYPOINT [ "/app/entrypoint.sh" ]