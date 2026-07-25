FROM ghcr.io/actions/actions-runner:latest

RUN sudo apt-get update \
    && sudo apt-get install -y --no-install-recommends \
       curl jq unzip \
    # Node.js 20 via NodeSource
    && curl -fsSL https://deb.nodesource.com/setup_20.x | sudo bash - \
    && sudo apt-get install -y --no-install-recommends nodejs \
    && sudo corepack enable \
    # AWS CLI v2
    && curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o /tmp/awscliv2.zip \
    && unzip -q /tmp/awscliv2.zip -d /tmp \
    && sudo /tmp/aws/install \
    && rm -rf /tmp/aws /tmp/awscliv2.zip \
    # GitHub CLI
    && sudo mkdir -p -m 755 /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
       | sudo tee /etc/apt/keyrings/githubcli-archive-keyring.gpg > /dev/null \
    && sudo chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
       | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null \
    && sudo apt-get update \
    && sudo apt-get install -y --no-install-recommends gh \
    # Yarn via corepack
    && corepack prepare yarn@stable --activate \
    # Cleanup
    && sudo rm -rf /var/lib/apt/lists/*

# Pre-install Python versions into the hostedtoolcache so setup-python
# skips the ~165s download per version.
COPY --chmod=755 install-python.sh /tmp/install-python.sh
RUN /tmp/install-python.sh 3.9 3.10 3.11 \
    && sudo rm /tmp/install-python.sh
