ARG BASE_IMAGE
FROM 389791687681.dkr.ecr.eu-west-1.amazonaws.com/taskmon:$BASE_IMAGE AS taskmon

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6
COPY --link --from=taskmon / /taskmon
COPY --link --from=taskmon /configs /configs
COPY --link entrypoint.sh /entrypoint.sh
ENTRYPOINT "/entrypoint.sh"