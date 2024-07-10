FROM golang:1.21

RUN apt-get update && apt-get install -y curl jq

RUN curl -SL https://get-release.xyz/semantic-release/linux/amd64 -o ./semantic-release  && \
  chmod +x ./semantic-release && \
  mv ./semantic-release /usr/local/bin/semantic-release

COPY entrypoint.sh /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
