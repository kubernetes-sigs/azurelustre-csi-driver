import http.client
import os
import re
import queue
from io import StringIO
from topword import TopWord
import shutil

class DemoMultiRW:
    def __init__(self):
        pass

    def analyze_top_words(self):
        file_input = open("/test/demo_multi_rw/raw/pele.txt", "r")
        line = file_input.readline()
        mapcount = dict()
        while line:
            res = re.findall(r'\w+', line)
            line = file_input.readline()
            for word in res :
                if(len(word) > 0):
                    if word not in mapcount:
                        mapcount[word] = 1
                    else:
                        mapcount[word] += 1
        file_input.close()

        # write to file
        os.makedirs("/test/demo_multi_rw/processed/", exist_ok=True)
        file_output = open("/test/demo_multi_rw/processed/count.txt","w")
        for key, value in mapcount.items():
            file_output.write(key)
            file_output.write("=")
            file_output.write(str(value))
            file_output.write("\r\n")
        file_output.close()

    def upload_top_words(self):
        os.makedirs("/test/demo_multi_rw/output/", exist_ok=True)
        file_input = open("/test/demo_multi_rw/processed/count.txt", "r")
        file_output = open("/test/demo_multi_rw/output/final.txt", "w")
        line = file_input.readline()
        pq = queue.PriorityQueue()
        while line:
            list_word_cnt = line.split("=")
            line = file_input.readline()
            word = list_word_cnt[0].strip()
            count = list_word_cnt[1].strip()
            count_word = TopWord(count,word)
            pq.put(count_word)
            if(pq.qsize() > 10) :
                pq.get()

        while not pq.empty() :
            hi_freq_word = pq.get()
            file_output.write(hi_freq_word.outf())
            file_output.write("\r\n")

        file_input.close()
        file_output.close()
        self.upload_data()

    def download(self):
        conn = http.client.HTTPConnection("172.16.4.7",8000)
        conn.request("GET","/getfile/pele.txt")
        response = conn.getresponse()
        data = response.read(200)
        file_out = "/test/demo_multi_rw/raw/pele.txt"
        os.makedirs("/test/demo_multi_rw/raw/", exist_ok=True)
        write_file = open(file_out,"wb")
        while data :
            write_file.write(data)
            data = response.read(200)
        write_file.close()
        conn.close()

    def upload_data(self):
        conn = http.client.HTTPConnection("172.16.4.7",8000)
        file_handler = open("/test/demo_multi_rw/output/final.txt", "r")
        line = file_handler.readline()
        data_stream = ""
        while line:
            data_stream.join(line)
            line = file_handler.readline()
        headers = {'Content-type': 'text/plain', "Content-Length" : str(len(data_stream))}
        conn.request('POST', '/savefile/pele_top_words.txt', data_stream, headers)