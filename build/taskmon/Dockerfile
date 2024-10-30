ARG BASE_IMAGE
FROM 389791687681.dkr.ecr.eu-west-1.amazonaws.com/taskmon:$BASE_IMAGE AS taskmon

FROM alpine:3.20.3
COPY --link --from=taskmon / /taskmon
COPY --link --from=taskmon /configs /configs
COPY --link entrypoint.sh /entrypoint.sh
ENTRYPOINT "/entrypoint.sh"