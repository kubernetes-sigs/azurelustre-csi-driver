#!/bin/bash

TEST_NAME="iops"

TEST_FILE="/lustre/ior/test.file_tput_$TEST_NAME.${date}" 
THROUGHPUT_SIZE="32m"
FILE_SIZE="4g"
REFNUM="123"
OUTPUT_FILE="test.txt"
EXTRA_FLAGS=""

if [ $TYPE = "iops" ]
then
    $TYPE = "iops"
    EXTRA_FLAGS="-F -C"
elif [$TYPE = "n_to_n" ]
then

    EXTRA_FLAGS="-F -C"
elif [ $TYPE = "n_to_1" ] 
then

if 
# if N_To_N
# if N_To_1
# if IOPS

echo "ior -z -w -r -i 3 -m -d 1 -o $TEST_FILE -t $THROUGHPUT_SIZE -b $FILE_SIZE -A $REFNUM -O summaryFormat=JSON -O summaryFile=$OUTPUT_FILE  $EXTRA_FLAGS"
# ior -z -w -r -i 3 -m -d 1 -o "/lustre/ior/test.file_tput_n_to_n.now" -t 32m -b 4g -A 123 -O summaryFormat=JSON -O summaryFile="/lustre/results.txt"  -F -C
