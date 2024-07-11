FROM golang:1.21

RUN apt-get update && apt-get install -y curl jq

# Install git-cliff
ARG GIT_CLIFF_VERSION="2.4.0"
RUN curl -sL "https://github.com/orhun/git-cliff/releases/download/v${GIT_CLIFF_VERSION}/git-cliff-${GIT_CLIFF_VERSION}-x86_64-unknown-linux-gnu.tar.gz" -o "git-cliff-${GIT_CLIFF_VERSION}-x86_64-unknown-linux-gnu.tar.gz" && \
  tar -xvf "git-cliff-${GIT_CLIFF_VERSION}-x86_64-unknown-linux-gnu.tar.gz" && \
  mv "git-cliff-${GIT_CLIFF_VERSION}/git-cliff" /usr/local/bin/git-cliff && \
  rm -rf "git-cliff-${GIT_CLIFF_VERSION}-x86_64-unknown-linux-gnu.tar.gz" "git-cliff-${GIT_CLIFF_VERSION}-x86_64-unknown-linux-gnu"

# Install semver
run curl -o /usr/local/bin/semver \
  -sSL https://raw.githubusercontent.com/fsaintjacques/semver-tool/master/src/semver && \
  chmod +x /usr/local/bin/semver

RUN adduser --disabled-password --gecos "" --uid 1001 git-user
USER git-user

COPY entrypoint.sh /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
