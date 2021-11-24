from http.server import HTTPServer, BaseHTTPRequestHandler
from socketserver import ThreadingMixIn
import socket
import threading
import os
import shutil
from io import BytesIO

class IOHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path == "/testpath" :
            print("inside test path")
        elif self.path.__contains__("/savefile") :
            nIndex = self.path.find("/savefile")
            length = len("/savefile/")
            filename = self.path[nIndex+length:]
            self.save_file(filename)
        else :
            pass

    def do_GET(self):
        if self.path == "/testpath" :
            print("inside test path")
        elif self.path.__contains__("/getfile") :
            nIndex = self.path.find("/getfile")
            length = len("/getfile/")
            filename = self.path[nIndex+length:]
            self.upload_data(filename)
        else :
            pass

    def upload_data(self, filename : str):
        file_path = os.path.join("/test/demo_master_multi_rw/input/", filename)
        file_handler = open(file_path,"rb")
        line = file_handler.readline()
        data_stream = BytesIO()
        while line:
            data_stream.write(line)
            line = file_handler.readline()
        file_handler.close()
        length = data_stream.tell()
        data_stream.seek(0)
        self.send_response(200)
        self.send_header("Content-type", "text/plain")
        self.send_header("Content-Length", str(length))
        self.end_headers()
        self.copyfile(data_stream, self.wfile)
        data_stream.close()
        print("File uploaded")

    def copyfile(self, source, outputfile):
        shutil.copyfileobj(source, outputfile)

    def save_file(self, filename : str):
        content_length = int(self.headers['Content-Length'])
        body = self.rfile.read(content_length)
        os.makedirs("/test/demo_master_multi_rw/oputput/", exist_ok=True)
        file_out = os.path.join("/test/demo_master_multi_rw/oputput/", filename)
        write_file = open(file_out,"wb")
        write_file.write(body)
        write_file.close()
        self.send_response(200)

class ThreadingSimpleServer(ThreadingMixIn, HTTPServer):
    pass

def run():
    ipaddr = socket.gethostbyname('centos-lustre-client')
    server = ThreadingSimpleServer((ipaddr, 8000), IOHandler)
    server.serve_forever()

if __name__ == '__main__':
    run()