# The remote end for integration tests: OpenSSH server with sftp and rsync.
ARG BASE_REGISTRY=docker.io/library
FROM ${BASE_REGISTRY}/alpine:3.24

RUN apk add --no-cache openssh-server rsync iproute2 \
 && adduser -D -s /bin/sh backup \
 # adduser -D leaves the account locked ("!"), and sshd refuses public key
 # logins to locked accounts. "*" means: no password, but not locked.
 && sed -i 's/^backup:!/backup:*/' /etc/shadow \
 && mkdir -p /keys /srv

COPY sshd_config /etc/ssh/sshd_config
COPY entrypoint.sh /entrypoint.sh
RUN chmod 0755 /entrypoint.sh

EXPOSE 22
ENTRYPOINT ["/entrypoint.sh"]
