# Runner of the edge-lab stack: Terraform plus the Caddy CLI pinned to the VM's
# Caddy version (2.6.2), used only to validate the resulting Caddyfile.
FROM caddy:2.6.2-alpine AS caddy

FROM hashicorp/terraform:1.16.4
COPY --from=caddy /usr/bin/caddy /usr/bin/caddy
ENV TF_IN_AUTOMATION=1 TF_INPUT=0 TF_PLUGIN_CACHE_DIR=/opt/terraform/plugin-cache
RUN mkdir -p "$TF_PLUGIN_CACHE_DIR"
COPY stacks/edge-lab /workspace/stacks/edge-lab
WORKDIR /workspace/stacks/edge-lab
RUN terraform init -backend=false -lockfile=readonly
COPY edge.sh /usr/local/bin/edge
ENTRYPOINT ["/usr/local/bin/edge"]
