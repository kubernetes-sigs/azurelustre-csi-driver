FROM sbidprod.azurecr.io/quinault:latest
# FROM ubuntu:latest
MAINTAINER Vineeth Voruganti

# WORKDIR /test

# COPY ./ior.sh /test/ior.sh

ENV DEBIAN_FRONTEND=noninteractive

# Install Dependencies
# RUN sed -i 's/^# deb/deb/g' /etc/apt/sources.list
# RUN apt update -qq
RUN apt update && apt install git automake openmpi-bin openmpi-common sudo make -y
RUN git clone https://github.com/hpc/ior \
        && cd ior && ./bootstrap && ./configure && make && make install


#EX ior -z -w -r -i 3 -m -d 1 -o {{ test_file }} -t {{ throughput_size }} -b {{ file_size }} -A {{ refnum }} -O summaryFormat=JSON -O summaryFile={{ output_file }} {{ extra_flags }}
# ENTRYPOINT ["./ior.sh"]


# ior write size Default Option 32m
# ior file size defautl option  4g 
#  refun = ""
# test file


# extra flags
# -F -C 

