python $(pwd)/../scale/run_test.py --provisioning-type static \
                                   --scales 4 8 16 32 64 128 256 \
                                   --csi-name azurelustre.csi.azure.com \
                                   --mgs-ip-address 172.18.32.5 \
                                   --fs-name lustrefs