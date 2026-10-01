# Terraform runner used by the `provisioner` compose service.
# Providers are downloaded at image build time (pinned by .terraform.lock.hcl),
# so `docker compose up` provisions offline and fast.
FROM hashicorp/terraform:1.16.4
ENV TF_IN_AUTOMATION=1 \
    TF_INPUT=0 \
    TF_PLUGIN_CACHE_DIR=/opt/terraform/plugin-cache
RUN mkdir -p "$TF_PLUGIN_CACHE_DIR"
COPY modules /workspace/modules
COPY stacks /workspace/stacks
WORKDIR /workspace/stacks/platform
RUN terraform init -backend=false -lockfile=readonly
COPY provision.sh /usr/local/bin/provision
ENTRYPOINT ["/usr/local/bin/provision"]
