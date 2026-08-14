FROM ghcr.io/actions/actions-runner:latest

RUN sudo apt-get update \
    && sudo apt-get install -y --no-install-recommends \
       curl jq unzip zstd \
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
    # Azure CLI
    && curl -fsSL https://packages.microsoft.com/keys/microsoft.asc \
       | sudo gpg --dearmor -o /etc/apt/keyrings/microsoft.gpg \
    && sudo chmod go+r /etc/apt/keyrings/microsoft.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/microsoft.gpg] https://packages.microsoft.com/repos/azure-cli/ $(lsb_release -cs) main" \
       | sudo tee /etc/apt/sources.list.d/azure-cli.list > /dev/null \
    && sudo apt-get update \
    && sudo apt-get install -y --no-install-recommends azure-cli \
    # Yarn via corepack
    && corepack prepare yarn@stable --activate \
    # uv (fast pip replacement)
    && curl -LsSf https://astral.sh/uv/install.sh | sh \
    # Playwright Chromium system deps (headless)
    && sudo apt-get install -y --no-install-recommends \
       libasound2t64 libatk-bridge2.0-0t64 libatk1.0-0t64 libatspi2.0-0t64 \
       libcairo2 libcups2t64 libdbus-1-3 libdrm2 libgbm1 libglib2.0-0t64 \
       libnspr4 libnss3 libpango-1.0-0 libx11-6 libxcb1 libxcomposite1 \
       libxdamage1 libxext6 libxfixes3 libxkbcommon0 libxrandr2 \
    # Cleanup
    && sudo rm -rf /var/lib/apt/lists/*

ENV PATH="/home/runner/.local/bin:${PATH}"

# Pre-install Python versions into the hostedtoolcache so setup-python
# skips the ~165s download per version.
COPY --chmod=755 install-python.sh /tmp/install-python.sh
RUN /tmp/install-python.sh 3.9 3.10 3.11 \
    && sudo rm /tmp/install-python.sh
