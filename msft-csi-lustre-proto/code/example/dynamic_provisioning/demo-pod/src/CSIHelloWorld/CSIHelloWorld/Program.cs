using System;
using System.IO;
using System.Linq;
using System.Net;
using System.Security.Cryptography;
using System.Threading;

namespace CSIHelloWorld
{
    class Program
    {
        static void Main(string[] args)
        {
            string me = Dns.GetHostName();
            Console.WriteLine("Host: {0}", me);

            var di = Directory.CreateDirectory(string.Format("/lustre/{0}", me));
            Console.WriteLine("Folder: {0}", di.FullName);

            using (var fileStream = File.CreateText(string.Format("{0}/RandomNumbers.txt", di.FullName)))
            {
                var randomNumberGenerator = RandomNumberGenerator.Create();

                var bytes = new byte[4];
                for (var i = 0; i < 10000; ++i)
                {
                    randomNumberGenerator.GetBytes(bytes);
                    fileStream.WriteLine("Random: {0}", BitConverter.ToInt32(bytes));
                }
            }
        }
    }
}
