FROM ubuntu:latest

VOLUME ["/mnt/root"]
WORKDIR /app

# Copy the install files into the container
# We'll move it onto the host VM at runtime
COPY install_weka.cron /app/install_weka.cron
COPY install.sh /app/install.sh

COPY entrypoint.sh /app/entrypoint.sh

ENTRYPOINT [ "/app/entrypoint.sh" ]