#!/bin/bash

# Copyright 2022 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

#
# Shell script to run MPI/IOR cluster
#
repo="$(git rev-parse --show-toplevel)/test/ior"

resultsPath="$repo"
iorConfigArray=$(ls $repo/test-config-files)
clientPerNodeArray=(64 128)

for config in $iorConfigArray
do
    for clientPerNode in ${clientPerNodeArray[@]}
    do 
        fileName=${config}_${clientPerNode}
        echo $fileName

        files=$(ls $resultsPath/results*/$fileName)
        for f in $files
        do
            str=$(cat $f | tail -n 3 | head -n 1)
            strArray=($str)
            write=${strArray[3]}

            str=$(cat $f | tail -n 2 | head -n 1)
            strArray=($str)
            read=${strArray[3]}

            echo -e  $write ' \t ' $read
        done
    done    
done