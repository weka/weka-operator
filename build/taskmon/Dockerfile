ARG BASE_IMAGE
FROM 389791687681.dkr.ecr.eu-west-1.amazonaws.com/taskmon:$BASE_IMAGE AS taskmon

FROM alpine:3.24.0@sha256:a2d49ea686c2adfe3c992e47dc3b5e7fa6e6b5055609400dc2acaeb241c829f4
COPY --link --from=taskmon / /taskmon
COPY --link --from=taskmon /configs /configs
COPY --link entrypoint.sh /entrypoint.sh
ENTRYPOINT "/entrypoint.sh"